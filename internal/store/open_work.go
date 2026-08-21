package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OpenWorkItem is one entry tagged for autonomous pick-up. It bundles
// the underlying entry plus the parsed convention tags so an agent
// scanning for work doesn't have to re-parse.
type OpenWorkItem struct {
	Entry     *Entry
	Effort    string   // "S" | "M" | "L" | ""
	Skills    []string // role names declared via `skill:<role>`
	Needs     []string // capabilities declared via `needs:<capability>`
	Priority  string   // "low" | "" — derived from `priority:<level>` tag if present
	ClaimedBy string   // instance_id if `claimed:<id>` tag present; empty when open
}

// parseOpenWorkTags extracts the convention metadata from a tag slice.
// See decision entry D-JNUYH6 for the conventions.
func parseOpenWorkTags(tags []string) (item OpenWorkItem) {
	for _, t := range tags {
		switch {
		case strings.HasPrefix(t, "effort:"):
			item.Effort = strings.TrimPrefix(t, "effort:")
		case strings.HasPrefix(t, "skill:"):
			item.Skills = append(item.Skills, strings.TrimPrefix(t, "skill:"))
		case strings.HasPrefix(t, "needs:"):
			item.Needs = append(item.Needs, strings.TrimPrefix(t, "needs:"))
		case strings.HasPrefix(t, "priority:"):
			item.Priority = strings.TrimPrefix(t, "priority:")
		case strings.HasPrefix(t, "claimed:"):
			item.ClaimedBy = strings.TrimPrefix(t, "claimed:")
		}
	}
	return item
}

// ListOpenWork returns entries tagged "open" (i.e., currently
// unclaimed). Optional filters: `role` (matches `skill:<role>` tag),
// `effort` (matches `effort:<S|M|L>`). All filters are AND-combined.
//
// Designed for autonomous agents to discover work — see entry
// `X-VUXYRR` (オープンアイディア自走実装ループ) for the concept.
func (s *Store) ListOpenWork(ctx context.Context, role, effort string) ([]*OpenWorkItem, error) {
	required := []string{"open"}
	if role != "" {
		required = append(required, "skill:"+role)
	}
	if effort != "" {
		required = append(required, "effort:"+effort)
	}
	hits, err := s.LookupByTags(ctx, required, "all", 100)
	if err != nil {
		return nil, err
	}
	out := make([]*OpenWorkItem, 0, len(hits))
	for _, h := range hits {
		e, err := s.GetEntry(ctx, h.EntryID)
		if err != nil {
			continue
		}
		// Filter out non-ACTIVE entries (LookupByTags doesn't exclude
		// archived/superseded by default).
		if e.Status != "ACTIVE" {
			continue
		}
		item := parseOpenWorkTags(e.Tags)
		item.Entry = e
		out = append(out, &item)
	}
	return out, nil
}

// ClaimOpenWork atomically claims an entry tagged "open" for the given
// instance:
//
//  1. Verifies the entry is currently tagged "open" (otherwise returns
//     ErrAlreadyExists — someone else got there first)
//  2. Inserts a librarian_tasks row with metadata.implements_entry_id
//     pointing at the entry, status=IN_PROGRESS, assigned_to=instanceID
//  3. Swaps the "open" tag for "claimed:<instanceID>"
//
// Returns the new task_id.
func (s *Store) ClaimOpenWork(ctx context.Context, entryID, role, instanceID, effort string) (string, error) {
	if !ValidLibrarianRole(role) {
		return "", fmt.Errorf("%w: invalid role %q", ErrInvalidInput, role)
	}
	if strings.TrimSpace(instanceID) == "" {
		return "", fmt.Errorf("%w: instance_id required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Visibility narrows the open-tag check itself: a hidden entry takes
	// exactly the missing-entry path (ErrAlreadyExists, "not tagged
	// open") so claim probing cannot distinguish restricted ids from
	// nonexistent ones.
	hasOpenQ := `SELECT COUNT(*) FROM tags t JOIN entries e ON e.id = t.entry_id
		WHERE t.entry_id = ? AND t.tag = 'open'`
	hasOpenArgs := []any{entryID}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		hasOpenQ += ` AND ` + cond
		hasOpenArgs = append(hasOpenArgs, condArgs...)
	}
	var hasOpen int
	if err := tx.QueryRowContext(ctx, hasOpenQ, hasOpenArgs...).Scan(&hasOpen); err != nil {
		return "", translateErr(err)
	}
	if hasOpen == 0 {
		return "", fmt.Errorf("%w: entry not tagged open (already claimed or never was)", ErrAlreadyExists)
	}

	var title string
	if err := tx.QueryRowContext(ctx,
		`SELECT title FROM entries WHERE id = ?`, entryID).Scan(&title); err != nil {
		return "", translateErr(err)
	}

	taskID := newLibrarianID("task")
	metaJSON, _ := json.Marshal(map[string]string{
		"implements_entry_id": entryID,
		"effort":              effort,
	})
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO librarian_tasks(task_id, role, title, description, priority, status, assigned_to, started_at, metadata)
		VALUES (?, ?, ?, ?, 100, 'IN_PROGRESS', ?, ?, ?)`,
		taskID, role, "impl: "+title, "Implements entry "+entryID,
		instanceID, now, string(metaJSON)); err != nil {
		return "", translateErr(err)
	}

	// Swap tags atomically: drop "open", add "claimed:<instance>".
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM tags WHERE entry_id = ? AND tag = 'open'`, entryID); err != nil {
		return "", translateErr(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tags(entry_id, tag, source) VALUES (?, ?, 'open_work')
		ON CONFLICT DO NOTHING`,
		entryID, "claimed:"+instanceID); err != nil {
		return "", translateErr(err)
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return taskID, nil
}

// ReleaseOpenWork reverses a claim: drops `claimed:<instance>`, restores
// `open`, and cancels the linked librarian_task. Useful when an agent
// realises the work is beyond its capability and wants to return it to
// the pool.
func (s *Store) ReleaseOpenWork(ctx context.Context, entryID, instanceID string) error {
	tag := "claimed:" + instanceID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireVisibleEntry(ctx, tx, entryID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM tags WHERE entry_id = ? AND tag = ?`, entryID, tag)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: entry not claimed by %s", ErrNotFound, instanceID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tags(entry_id, tag, source) VALUES (?, 'open', 'open_work')
		ON CONFLICT DO NOTHING`, entryID); err != nil {
		return translateErr(err)
	}
	// Cancel the linked librarian_task. Match by metadata containing
	// the entry_id and assigned_to == instance.
	if _, err := tx.ExecContext(ctx, `
		UPDATE librarian_tasks
		SET status = 'CANCELLED', completed_at = ?
		WHERE assigned_to = ?
		  AND status = 'IN_PROGRESS'
		  AND metadata LIKE ?`,
		time.Now().UTC(), instanceID, `%"implements_entry_id":"`+entryID+`"%`); err != nil {
		return translateErr(err)
	}
	return tx.Commit()
}

// MarkOpenWorkMerged closes a claim as successful:
//  1. Drops `claimed:<instance>` tag, adds `merged`
//  2. Marks the linked librarian_task DONE with the given result text
//  3. Optionally creates a `resolved_by` relation from the implementing
//     entry (e.g., a new commit / PR / design doc entry) to this entry
//
// `implEntryID` may be empty when the work is documentation-only.
func (s *Store) MarkOpenWorkMerged(ctx context.Context, entryID, instanceID, result, implEntryID string) error {
	tag := "claimed:" + instanceID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireVisibleEntry(ctx, tx, entryID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM tags WHERE entry_id = ? AND tag = ?`, entryID, tag)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: entry not claimed by %s", ErrNotFound, instanceID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tags(entry_id, tag, source) VALUES (?, 'merged', 'open_work')
		ON CONFLICT DO NOTHING`, entryID); err != nil {
		return translateErr(err)
	}
	// Mark task DONE
	if _, err := tx.ExecContext(ctx, `
		UPDATE librarian_tasks
		SET status = 'DONE', completed_at = ?, result = ?
		WHERE assigned_to = ?
		  AND status = 'IN_PROGRESS'
		  AND metadata LIKE ?`,
		time.Now().UTC(), nullable(result), instanceID,
		`%"implements_entry_id":"`+entryID+`"%`); err != nil {
		return translateErr(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// `resolved_by` relation — outside the tx because CreateRelation
	// runs its own transaction (and the cross-entry FK can deadlock if
	// we nest).
	if implEntryID != "" {
		if err := s.CreateRelation(ctx, &Relation{
			FromID: implEntryID, ToID: entryID,
			RelType: "resolved_by",
			Source:  "open_work",
		}); err != nil {
			return err
		}
	}
	return nil
}
