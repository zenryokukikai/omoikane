package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Relation is a directed edge in the entry graph.
type Relation struct {
	FromID     string
	ToID       string
	RelType    string
	Confidence float64
	Source     string
	Notes      string
	CreatedAt  time.Time
}

// ValidRelType reports whether the given relation type is supported.
// Per §4.2 the canonical set is related / supersedes / conflicts_with /
// depends_on / see_also / duplicate_of / resolved_by.
func ValidRelType(t string) bool {
	switch t {
	case "related", "supersedes", "conflicts_with",
		"depends_on", "see_also", "duplicate_of", "resolved_by":
		return true
	}
	return false
}

// CreateRelation inserts a relation. When `rel_type=conflicts_with` is
// added between two entries, the design (Auto-supersede on contradiction,
// §13 Phase 3) requires marking one side SUPERSEDED automatically. We
// supersede the OLDER entry — the newer one is presumed to reflect the
// updated understanding.
func (s *Store) CreateRelation(ctx context.Context, r *Relation) error {
	if !ValidRelType(r.RelType) {
		return fmt.Errorf("%w: invalid rel_type %q", ErrInvalidInput, r.RelType)
	}
	if r.FromID == r.ToID {
		return fmt.Errorf("%w: cannot relate entry to itself", ErrInvalidInput)
	}
	if r.Confidence == 0 {
		r.Confidence = 1.0
	}
	if r.Source == "" {
		r.Source = "human"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relations(from_id, to_id, rel_type, confidence, source, notes)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_id, to_id, rel_type) DO UPDATE SET
		    confidence = excluded.confidence,
		    source     = excluded.source,
		    notes      = excluded.notes`,
		r.FromID, r.ToID, r.RelType, r.Confidence, r.Source, nullable(r.Notes)); err != nil {
		return translateErr(err)
	}

	// Auto-supersede: only when this is the first conflicts_with edge for
	// this pair (we still run the logic on conflict updates, but it is
	// idempotent for ALREADY-SUPERSEDED entries).
	if r.RelType == "conflicts_with" {
		if err := autoSupersedeOnConflict(ctx, tx, r.FromID, r.ToID); err != nil {
			return err
		}
	}
	if r.RelType == "supersedes" {
		// Explicit supersedes — mark the older side directly per
		// from_id supersedes to_id (from is the new winner).
		if err := markSuperseded(ctx, tx, r.ToID, r.FromID, "supersedes relation"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// autoSupersedeOnConflict marks the OLDER of (a, b) as SUPERSEDED. The
// other side is recorded in `superseded_by`. Idempotent — does nothing if
// either side is already SUPERSEDED/ARCHIVED.
func autoSupersedeOnConflict(ctx context.Context, tx *sql.Tx, a, b string) error {
	type info struct {
		status    string
		createdAt time.Time
	}
	load := func(id string) (info, error) {
		var i info
		err := tx.QueryRowContext(ctx,
			`SELECT status, created_at FROM entries WHERE id = ?`, id,
		).Scan(&i.status, &i.createdAt)
		return i, err
	}
	ai, err := load(a)
	if err != nil {
		return err
	}
	bi, err := load(b)
	if err != nil {
		return err
	}
	// Skip if either side already in a terminal state.
	if isTerminal(ai.status) || isTerminal(bi.status) {
		return nil
	}
	older, newer := a, b
	if bi.createdAt.Before(ai.createdAt) {
		older, newer = b, a
	}
	return markSuperseded(ctx, tx, older, newer, "auto-supersede on conflicts_with")
}

func isTerminal(status string) bool {
	switch status {
	case "SUPERSEDED", "ARCHIVED", "DUPLICATE", "RESOLVED":
		return true
	}
	return false
}

func markSuperseded(ctx context.Context, tx *sql.Tx, oldID, newID, reason string) error {
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `
		UPDATE entries
		SET status = 'SUPERSEDED', valid_to = ?, superseded_by = ?,
		    invalidation_reason = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND status != 'SUPERSEDED'`,
		now, newID, reason, now, oldID,
	)
	return translateErr(err)
}

// DeleteRelation removes a single edge. Returns ErrNotFound if no such
// edge exists.
func (s *Store) DeleteRelation(ctx context.Context, fromID, toID, relType string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM relations WHERE from_id = ? AND to_id = ? AND rel_type = ?`,
		fromID, toID, relType)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRelationsFrom returns outgoing edges for the given entry.
func (s *Store) ListRelationsFrom(ctx context.Context, entryID string) ([]*Relation, error) {
	return s.listRelations(ctx,
		`SELECT from_id, to_id, rel_type, confidence, source, COALESCE(notes,''), created_at
		 FROM relations WHERE from_id = ? ORDER BY created_at`, entryID)
}

// ListRelationsTo returns the backlinks pointing at the given entry.
func (s *Store) ListRelationsTo(ctx context.Context, entryID string) ([]*Relation, error) {
	return s.listRelations(ctx,
		`SELECT from_id, to_id, rel_type, confidence, source, COALESCE(notes,''), created_at
		 FROM relations WHERE to_id = ? ORDER BY created_at`, entryID)
}

func (s *Store) listRelations(ctx context.Context, q, id string) ([]*Relation, error) {
	rows, err := s.db.QueryContext(ctx, q, id)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Relation](rows, func(c rowScanner, r *Relation) error {
		return c.Scan(&r.FromID, &r.ToID, &r.RelType, &r.Confidence, &r.Source, &r.Notes, &r.CreatedAt)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Relation, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
