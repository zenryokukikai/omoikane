package store

// entries.go owns the entries table's CRUD and listing: create/update/
// soft-delete (each in a tx with a history snapshot), the point reads,
// the filtered listing, and the enrichment marker. The scan layer lives
// in entry_scan.go, history in entry_history.go, tags in entry_tags.go.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CreateEntry inserts a new entry plus its initial history snapshot (v1).
// Returns the assigned ID.
func (s *Store) CreateEntry(ctx context.Context, e *Entry) (string, error) {
	if e == nil {
		return "", ErrInvalidInput
	}
	if e.ProjectID == "" || e.Title == "" || e.Body == "" {
		return "", fmt.Errorf("%w: project_id, title, body are required", ErrInvalidInput)
	}
	if !ValidEntryType(e.Type) {
		return "", fmt.Errorf("%w: invalid type %q", ErrInvalidInput, e.Type)
	}
	if e.Status == "" {
		e.Status = string(StatusDraft)
	}
	if !ValidStatus(e.Status) {
		return "", fmt.Errorf("%w: invalid status %q", ErrInvalidInput, e.Status)
	}
	if e.BodyFormat == "" {
		e.BodyFormat = "markdown"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Project must exist.
	var exists string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE id = ?`, e.ProjectID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: project %q does not exist", ErrInvalidInput, e.ProjectID)
		}
		return "", err
	}

	// Space: default 'internal'; must exist AND be visible to the caller
	// (ErrNotFound either way — a hidden space is indistinguishable from
	// a missing one, per the no-existence-oracle rule).
	if e.SpaceID == "" {
		e.SpaceID = SpaceInternal
	}
	if err := requireVisibleSpace(ctx, tx, e.SpaceID); err != nil {
		return "", err
	}

	id := e.ID
	if id == "" {
		for i := 0; i < 5; i++ {
			id, err = newEntryID(e.Type)
			if err != nil {
				return "", err
			}
			var n int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, id).Scan(&n)
			if err == sql.ErrNoRows {
				break
			}
			if err != nil {
				return "", err
			}
			id = ""
		}
		if id == "" {
			return "", fmt.Errorf("failed to allocate unique entry id after retries")
		}
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO entries(
			id, project_id, type, title, status,
			symptom, root_cause, resolution, prohibited,
			attempted_approaches, observed_behavior, hypotheses,
			body, body_format, scope, metadata,
			valid_from, valid_to, superseded_by, invalidation_reason,
			enrichment_version, enrichment_at,
			created_at, updated_at, created_by, created_by_role,
			space_id, version
		) VALUES (?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?, ?,?, ?,?,?,?, ?, 1)`,
		id, e.ProjectID, e.Type, e.Title, e.Status,
		nullable(e.Symptom), nullable(e.RootCause), nullable(e.Resolution), nullable(e.Prohibited),
		nullable(e.AttemptedApproaches), nullable(e.ObservedBehavior), nullable(e.Hypotheses),
		e.Body, e.BodyFormat, nullableRaw(e.Scope), nullableRaw(e.Metadata),
		now, nullableTime(e.ValidTo), nullable(e.SupersededBy), nullable(e.InvalidationReason),
		e.EnrichmentVersion, nullableTime(e.EnrichmentAt),
		now, now, nullable(e.CreatedBy), nullable(e.CreatedByRole),
		e.SpaceID,
	)
	if err != nil {
		return "", translateErr(err)
	}

	tags := normaliseTags(e.Tags)
	if err := replaceTagsTx(ctx, tx, id, tags, sourceFromRole(e.CreatedByRole)); err != nil {
		return "", err
	}

	// Initial history snapshot v1.
	if err := writeHistoryTx(ctx, tx, id, 1, &Entry{
		Title:               e.Title,
		Status:              e.Status,
		Symptom:             e.Symptom,
		RootCause:           e.RootCause,
		Resolution:          e.Resolution,
		Prohibited:          e.Prohibited,
		AttemptedApproaches: e.AttemptedApproaches,
		ObservedBehavior:    e.ObservedBehavior,
		Hypotheses:          e.Hypotheses,
		Body:                e.Body,
		BodyFormat:          e.BodyFormat,
		Scope:               e.Scope,
		Metadata:            e.Metadata,
		ValidFrom:           now,
		ValidTo:             e.ValidTo,
		SupersededBy:        e.SupersededBy,
		InvalidationReason:  e.InvalidationReason,
	}, tags, now, e.CreatedBy, e.CreatedByRole, "initial create"); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	e.ID = id
	e.CreatedAt = now
	e.UpdatedAt = now
	e.ValidFrom = now
	e.Version = 1
	return id, nil
}

// EntriesExist returns a map of {id → true} for every id in `ids` that
// has a row in `entries` (regardless of status). Missing ids are
// absent from the map (not present with value=false), so the caller's
// natural `map[id]` check returns the zero value `false`.
//
// Bulk check used by renderers that need to decide whether `[[L-XXX]]`
// references should become live links or muted "broken reference"
// indicators. One SQL round-trip regardless of len(ids).
//
// Space-aware (issue #60 slice 5): under a restricted ctx an entry
// outside the visible spaces reports "does not exist" — rendering it as
// a live link would be an existence oracle for hidden ids.
func (s *Store) EntriesExist(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT id FROM entries WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetEntry returns the current state. An entry outside the ctx's
// visible spaces is ErrNotFound — indistinguishable from a missing one.
func (s *Store) GetEntry(ctx context.Context, id string) (*Entry, error) {
	q := entrySelectSQL + ` WHERE e.id = ?`
	args := []any{id}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	e, err := scanEntryRow(s.db.QueryRowContext(ctx, q, args...))
	if err != nil {
		return nil, err
	}
	tags, err := s.getEntryTags(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Tags = tags
	return e, nil
}

// ListEntries returns (entries, total) for pagination. Total ignores
// limit/offset and counts the full filter result.
func (s *Store) ListEntries(ctx context.Context, f EntryFilter) ([]*Entry, int, error) {
	conds, args, joinTag := buildListConditions(f)
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	// Explicit space narrowing (f.SpaceID) — composed through
	// SpaceFilter, the single composition point, never hand-written.
	// ANDed with the visibility predicate above: 視界∩指定.
	if f.SpaceID != "" {
		cond, condArgs := SpaceFilter("e", []string{f.SpaceID})
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}

	// Count (no limit/offset).
	countSQL := "SELECT COUNT(*) FROM entries e" + joinTag
	if len(conds) > 0 {
		countSQL += " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	q := entrySelectSQL + joinTag
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	if f.OldestFirst {
		q += " ORDER BY e.updated_at ASC LIMIT ? OFFSET ?"
	} else {
		q += " ORDER BY e.updated_at DESC LIMIT ? OFFSET ?"
	}
	args2 := append(append([]any{}, args...), limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args2...)
	if err != nil {
		return nil, 0, err
	}
	entries, err := mapRows[Entry](rows, func(c rowScanner, e *Entry) error {
		got, err := scanEntry(c)
		if err != nil {
			return err
		}
		*e = *got
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*Entry, len(entries))
	ids := make([]string, len(entries))
	for i := range entries {
		out[i] = &entries[i]
		ids[i] = entries[i].ID
	}
	if err := s.attachTags(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// UpdateEntry applies the patch atomically with OCC. If
// p.ExpectedVersion > 0 it must match the current version; otherwise we
// return ErrVersionMismatch (the API layer maps this to HTTP 409).
//
// Returns the new version number and the fully reconstructed Entry.
func (s *Store) UpdateEntry(ctx context.Context, id string, p EntryPatch) (int, *Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	// Load current state inside the tx.
	cur, err := loadEntryTx(ctx, tx, id)
	if err != nil {
		return 0, nil, err
	}

	if p.ExpectedVersion > 0 && p.ExpectedVersion != cur.Version {
		return 0, nil, fmt.Errorf("%w: current=%d expected=%d",
			ErrVersionMismatch, cur.Version, p.ExpectedVersion)
	}

	// Apply patch onto a copy.
	merged := *cur
	if p.Title != nil {
		if *p.Title == "" {
			return 0, nil, fmt.Errorf("%w: title cannot be empty", ErrInvalidInput)
		}
		merged.Title = *p.Title
	}
	if p.Status != nil {
		if !ValidStatus(*p.Status) {
			return 0, nil, fmt.Errorf("%w: invalid status %q", ErrInvalidInput, *p.Status)
		}
		merged.Status = *p.Status
	}
	if p.Symptom != nil {
		merged.Symptom = *p.Symptom
	}
	if p.RootCause != nil {
		merged.RootCause = *p.RootCause
	}
	if p.Resolution != nil {
		merged.Resolution = *p.Resolution
	}
	if p.Prohibited != nil {
		merged.Prohibited = *p.Prohibited
	}
	if p.AttemptedApproaches != nil {
		merged.AttemptedApproaches = *p.AttemptedApproaches
	}
	if p.ObservedBehavior != nil {
		merged.ObservedBehavior = *p.ObservedBehavior
	}
	if p.Hypotheses != nil {
		merged.Hypotheses = *p.Hypotheses
	}
	if p.Body != nil {
		if *p.Body == "" {
			return 0, nil, fmt.Errorf("%w: body cannot be empty", ErrInvalidInput)
		}
		merged.Body = *p.Body
	}
	if p.BodyFormat != nil {
		merged.BodyFormat = *p.BodyFormat
	}
	if p.Scope != nil {
		merged.Scope = *p.Scope
	}
	if p.Metadata != nil {
		merged.Metadata = *p.Metadata
	}

	now := time.Now().UTC()
	newVersion := cur.Version + 1
	merged.Version = newVersion
	merged.UpdatedAt = now

	_, err = tx.ExecContext(ctx, `
		UPDATE entries SET
		  title = ?, status = ?,
		  symptom = ?, root_cause = ?, resolution = ?, prohibited = ?,
		  attempted_approaches = ?, observed_behavior = ?, hypotheses = ?,
		  body = ?, body_format = ?,
		  scope = ?, metadata = ?,
		  updated_at = ?, version = ?
		WHERE id = ? AND version = ?`,
		merged.Title, merged.Status,
		nullable(merged.Symptom), nullable(merged.RootCause), nullable(merged.Resolution), nullable(merged.Prohibited),
		nullable(merged.AttemptedApproaches), nullable(merged.ObservedBehavior), nullable(merged.Hypotheses),
		merged.Body, merged.BodyFormat,
		nullableRaw(merged.Scope), nullableRaw(merged.Metadata),
		now, newVersion,
		id, cur.Version,
	)
	if err != nil {
		return 0, nil, translateErr(err)
	}

	// Tags
	var tagsForSnapshot []string
	if p.Tags != nil {
		tagsForSnapshot = normaliseTags(*p.Tags)
		if err := replaceTagsTx(ctx, tx, id, tagsForSnapshot, sourceFromRole(p.ChangedByRole)); err != nil {
			return 0, nil, err
		}
	} else {
		// Snapshot the current tag set via the shared helper.
		got, err := loadTagsTx(ctx, tx, id)
		if err != nil {
			return 0, nil, err
		}
		tagsForSnapshot = got
	}
	merged.Tags = tagsForSnapshot

	if err := writeHistoryTx(ctx, tx, id, newVersion, &merged, tagsForSnapshot,
		now, p.ChangedBy, p.ChangedByRole, p.ChangeSummary); err != nil {
		return 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return newVersion, &merged, nil
}

// SoftDeleteEntry marks the entry archived and sets valid_to=NOW. Idempotent.
// Bumps version and writes a history row so as-of queries see the archived
// state.
func (s *Store) SoftDeleteEntry(ctx context.Context, id, changedBy, changedByRole string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cur, err := loadEntryTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur.Status == string(StatusArchived) {
		// Already archived — idempotent success.
		return tx.Commit()
	}
	now := time.Now().UTC()
	newVersion := cur.Version + 1
	cur.Status = string(StatusArchived)
	cur.ValidTo = &now
	if cur.InvalidationReason == "" {
		cur.InvalidationReason = "soft delete"
	}
	cur.Version = newVersion
	cur.UpdatedAt = now

	if _, err := tx.ExecContext(ctx, `
		UPDATE entries
		SET status = 'ARCHIVED', valid_to = ?, invalidation_reason = ?,
		    updated_at = ?, version = ?
		WHERE id = ?`,
		now, cur.InvalidationReason, now, newVersion, id); err != nil {
		return translateErr(err)
	}

	tags, err := loadTagsTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := writeHistoryTx(ctx, tx, id, newVersion, cur, tags, now,
		changedBy, changedByRole, "soft delete (ARCHIVED)"); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEnrichment marks an entry as enriched at the given version. Does NOT
// bump the entry's `version` column (that is reserved for OCC of body
// changes).
func (s *Store) SetEnrichment(ctx context.Context, id string, version int) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE entries SET enrichment_version = ?, enrichment_at = CURRENT_TIMESTAMP WHERE id = ?`,
		version, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func buildListConditions(f EntryFilter) (conds []string, args []any, joinTag string) {
	if f.ProjectID != "" {
		conds = append(conds, "e.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Type != "" {
		conds = append(conds, "e.type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		conds = append(conds, "e.status = ?")
		args = append(args, f.Status)
	}
	if !f.IncludeSuperseded {
		conds = append(conds, "e.status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')")
	}
	if f.Query != "" {
		conds = append(conds, "(e.title LIKE ? OR e.body LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q)
	}
	if f.Tag != "" {
		joinTag = " JOIN tags t ON t.entry_id = e.id "
		conds = append(conds, "t.tag = ?")
		args = append(args, f.Tag)
	}
	if f.Uncategorized {
		conds = append(conds,
			"NOT EXISTS (SELECT 1 FROM use_case_entries uce WHERE uce.entry_id = e.id)")
	}
	if f.NotProgressedByRole != "" {
		conds = append(conds,
			"NOT EXISTS (SELECT 1 FROM librarian_progress lp WHERE lp.entry_id = e.id AND lp.role = ?)")
		args = append(args, f.NotProgressedByRole)
	}
	return
}

// EntrySummary returns the cataloger's summary entry for `entryID`, if one
// exists. A summary is a librarian_meta entry with metadata.kind=cataloger_summary
// and metadata.source_entry_id matching the target.
//
// Phase 5 librarians write summaries as DRAFT (proposals), so we accept
// DRAFT / ACTIVE / INVESTIGATING — everything except SUPERSEDED / ARCHIVED /
// DUPLICATE. Otherwise no live summaries would ever be visible.
//
// Returns ErrNotFound when no cataloger summary has been written for this
// entry yet (the indexer / dashboard then falls back to the entry itself).
func (s *Store) EntrySummary(ctx context.Context, entryID string) (*Entry, error) {
	q := `
		SELECT id FROM entries
		 WHERE type = 'librarian_meta'
		   AND status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')
		   AND json_extract(metadata, '$.kind') = 'cataloger_summary'
		   AND json_extract(metadata, '$.source_entry_id') = ?`
	args := []any{entryID}
	// The summary is itself an entry — pick the newest VISIBLE one.
	if cond, condArgs := spaceCond(ctx, "entries"); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	q += `
		 ORDER BY created_at DESC
		 LIMIT 1`
	var id string
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&id)
	if err != nil {
		return nil, translateErr(err)
	}
	return s.GetEntry(ctx, id)
}
