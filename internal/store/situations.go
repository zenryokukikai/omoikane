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
	ID          string
	ProjectID   string
	Description string
	Domain      string
	CreatedAt   time.Time
	Metadata    string
}

// SituationEntry is a single (situation, entry) link.
type SituationEntry struct {
	SituationID string
	EntryID     string
	Relevance   float64
	Notes       string
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO situations(id, project_id, description, domain, metadata)
		VALUES (?, ?, ?, ?, ?)`,
		sit.ID, nullable(sit.ProjectID), sit.Description,
		nullable(sit.Domain), nullable(sit.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return sit.ID, nil
}

func (s *Store) GetSituation(ctx context.Context, id string) (*Situation, error) {
	var sit Situation
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(project_id,''), description, COALESCE(domain,''),
		       created_at, COALESCE(metadata,'')
		FROM situations WHERE id = ?`, id).Scan(
		&sit.ID, &sit.ProjectID, &sit.Description, &sit.Domain,
		&sit.CreatedAt, &sit.Metadata)
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
		created_at, COALESCE(metadata,'')
		FROM situations`)
	if projectID != "" {
		sb.WriteString(` WHERE project_id = ?`)
		args = append(args, projectID)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)
	r, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Situation](r, func(c rowScanner, sit *Situation) error {
		return c.Scan(&sit.ID, &sit.ProjectID, &sit.Description, &sit.Domain,
			&sit.CreatedAt, &sit.Metadata)
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
	rows, err := s.db.QueryContext(ctx, `
		SELECT situation_id, entry_id, COALESCE(relevance, 1.0), COALESCE(notes,'')
		FROM situation_entries WHERE situation_id = ?
		ORDER BY relevance DESC, entry_id`, situationID)
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
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}
	q := ftsTokenise(query)
	if q == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT se.entry_id, s.description, bm25(situations_fts) AS rank,
		       COALESCE(se.relevance, 1.0)
		FROM situations_fts
		JOIN situations s ON s.rowid = situations_fts.rowid
		JOIN situation_entries se ON se.situation_id = s.id
		WHERE situations_fts MATCH ?
		ORDER BY rank ASC
		LIMIT ?`, q, limit*3)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LookupHit](rows, func(c rowScanner, h *LookupHit) error {
		var rank, rel float64
		if err := c.Scan(&h.EntryID, &h.Phrase, &rank, &rel); err != nil {
			return err
		}
		h.Score = -rank * rel
		h.Source = "situation"
		return nil
	})
	if err != nil {
		return nil, err
	}
	hits := make([]*LookupHit, len(values))
	for i := range values {
		hits[i] = &values[i]
	}
	return dedupeKeepBestHit(hits, limit), nil
}

// DeleteSituation removes a situation and (via FK cascade) its entry links.
func (s *Store) DeleteSituation(ctx context.Context, id string) error {
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
