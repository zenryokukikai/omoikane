package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Situation is a reverse-dictionary heading — a short description of a
// situation the user might be in, that maps to one or more entries.
// Per docs/design.md §4.2.
type Situation struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Description string    `json:"description"`
	Domain      string    `json:"domain,omitempty"`
	SpaceID     string    `json:"space_id"`
	CreatedAt   time.Time `json:"created_at"`
	Metadata    string    `json:"metadata,omitempty"`
}

// SituationEntry is a single (situation, entry) link.
type SituationEntry struct {
	SituationID string  `json:"situation_id"`
	EntryID     string  `json:"entry_id"`
	Relevance   float64 `json:"relevance"`
	Notes       string  `json:"notes,omitempty"`
}

// newSituationID returns a 16-char hex situation identifier prefixed with
// "SIT-" so it is grep-distinct from entry/case IDs.
func newSituationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "SIT-" + hex.EncodeToString(b[:])
}

// CreateSituation inserts a new situation. ID is generated when empty.
func (s *Store) CreateSituation(ctx context.Context, sit *Situation) (string, error) {
	if strings.TrimSpace(sit.Description) == "" {
		return "", fmt.Errorf("%w: description required", ErrInvalidInput)
	}
	if sit.ID == "" {
		sit.ID = newSituationID()
	}
	// Space: default 'internal'; must exist AND be visible (hidden and
	// missing spaces are indistinguishable — same contract as CreateEntry).
	if sit.SpaceID == "" {
		sit.SpaceID = SpaceInternal
	}
	if err := requireVisibleSpace(ctx, s.db, sit.SpaceID); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO situations(id, project_id, description, domain, metadata, space_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sit.ID, nullable(sit.ProjectID), sit.Description,
		nullable(sit.Domain), nullable(sit.Metadata), sit.SpaceID)
	if err != nil {
		return "", translateErr(err)
	}
	return sit.ID, nil
}

func (s *Store) GetSituation(ctx context.Context, id string) (*Situation, error) {
	sqlQ := `
		SELECT id, COALESCE(project_id,''), description, COALESCE(domain,''),
		       space_id, created_at, COALESCE(metadata,'')
		FROM situations WHERE id = ?`
	args := []any{id}
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		sqlQ += " AND " + cond
		args = append(args, condArgs...)
	}
	var sit Situation
	err := s.db.QueryRowContext(ctx, sqlQ, args...).Scan(
		&sit.ID, &sit.ProjectID, &sit.Description, &sit.Domain,
		&sit.SpaceID, &sit.CreatedAt, &sit.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	return &sit, nil
}

// ListSituations returns situations, optionally filtered by project_id.
func (s *Store) ListSituations(ctx context.Context, projectID string, limit int) ([]*Situation, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, COALESCE(project_id,''), description, COALESCE(domain,''),
		space_id, created_at, COALESCE(metadata,'')
		FROM situations WHERE 1=1`)
	if projectID != "" {
		sb.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		sb.WriteString(` AND ` + cond)
		args = append(args, condArgs...)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)
	r, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Situation](r, func(c rowScanner, sit *Situation) error {
		return c.Scan(&sit.ID, &sit.ProjectID, &sit.Description, &sit.Domain,
			&sit.SpaceID, &sit.CreatedAt, &sit.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Situation, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// LinkEntryToSituation inserts (or refreshes) a situation_entries row.
// Idempotent — re-linking the same pair updates relevance/notes.
func (s *Store) LinkEntryToSituation(ctx context.Context, situationID, entryID string, relevance float64, notes string) error {
	if relevance == 0 {
		relevance = 1.0
	}
	// Single-space invariant (slice 3): the situation must be visible
	// and the entry must live in the situation's space (violation =
	// not-found, never a 403 oracle).
	if err := requireSameSpaceLink(ctx, s.db, "situations", situationID, entryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO situation_entries(situation_id, entry_id, relevance, notes)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(situation_id, entry_id) DO UPDATE SET
			relevance = excluded.relevance,
			notes = excluded.notes`,
		situationID, entryID, relevance, nullable(notes))
	return translateErr(err)
}

// UnlinkEntryFromSituation drops a single situation_entries row.
func (s *Store) UnlinkEntryFromSituation(ctx context.Context, situationID, entryID string) error {
	if err := requireVisibleAggregate(ctx, s.db, "situations", situationID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM situation_entries WHERE situation_id = ? AND entry_id = ?`,
		situationID, entryID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSituationEntries returns the entries linked to a situation, with
// their stored relevance/notes.
func (s *Store) ListSituationEntries(ctx context.Context, situationID string) ([]*SituationEntry, error) {
	// Defence in depth: the single-space invariant only guards NEW
	// links, so re-gate per entry (same as ListEntriesAtNode /
	// ListUseCaseEntries) — a cross-space row from the slice-2→3
	// deployment window must not surface.
	sqlQ := `
		SELECT situation_id, entry_id, COALESCE(relevance, 1.0), COALESCE(notes,'')
		FROM situation_entries WHERE situation_id = ?`
	args := []any{situationID}
	if cond, condArgs := visibleEntryExists(ctx, "situation_entries.entry_id"); cond != "" {
		sqlQ += ` AND ` + cond
		args = append(args, condArgs...)
	}
	sqlQ += ` ORDER BY relevance DESC, entry_id`
	rows, err := s.db.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[SituationEntry](rows, func(c rowScanner, se *SituationEntry) error {
		return c.Scan(&se.SituationID, &se.EntryID, &se.Relevance, &se.Notes)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*SituationEntry, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// LookupBySituation searches situations_fts for matching headings then
// returns the entries linked to those situations. The score reflects FTS
// rank weighted by stored relevance.
func (s *Store) LookupBySituation(ctx context.Context, query string, limit int) ([]*LookupHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	limit = clampLimit(limit, 10, 100)
	// Visibility narrows at the candidate stage: a situation may span
	// spaces (until slice 3 pins aggregates to one), but only entries the
	// caller can see may come back.
	hits, err := s.ftsLookup(ctx, query, limit, ftsLookupSpec{
		selectSQL: `
		SELECT se.entry_id, s.description, bm25(situations_fts) AS rank,
		       COALESCE(se.relevance, 1.0)
		FROM situations_fts
		JOIN situations s ON s.rowid = situations_fts.rowid
		JOIN situation_entries se ON se.situation_id = s.id
		JOIN entries e ON e.id = se.entry_id
		WHERE situations_fts MATCH ?`,
		scan: func(c rowScanner, h *LookupHit) error {
			var rank, rel float64
			if err := c.Scan(&h.EntryID, &h.Phrase, &rank, &rel); err != nil {
				return err
			}
			h.Score = -rank * rel
			h.Source = "situation"
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	if hits == nil {
		return nil, nil
	}
	return dedupeKeepBestHit(hits, limit), nil
}

// DeleteSituation removes a situation and (via FK cascade) its entry links.
func (s *Store) DeleteSituation(ctx context.Context, id string) error {
	if err := requireVisibleAggregate(ctx, s.db, "situations", id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM situations WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
