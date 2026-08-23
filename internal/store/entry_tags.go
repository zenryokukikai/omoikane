package store

// entry_tags.go owns the tags table for entries: normalisation, the
// tx-level replace/load primitives used by every entry write, and the
// batch attach used by list/search reads.

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// ReplaceTags is used by the enrichment writer to set tags with a specific
// source (llm/heuristic). It does not bump the entry's version.
func (s *Store) ReplaceTags(ctx context.Context, id string, tags []string, source string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceTagsTx(ctx, tx, id, normaliseTags(tags), source); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) getEntryTags(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM tags WHERE entry_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

func loadTagsTx(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tag FROM tags WHERE entry_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

func (s *Store) attachTags(ctx context.Context, entries []*Entry, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entry_id, tag FROM tags WHERE entry_id IN (`+placeholders+`) ORDER BY entry_id, tag`,
		args...)
	if err != nil {
		return err
	}
	pairs, err := collectPairs(rows)
	if err != nil {
		return err
	}
	byID := map[string][]string{}
	for _, p := range pairs {
		byID[p.First] = append(byID[p.First], p.Second)
	}
	for _, e := range entries {
		e.Tags = byID[e.ID]
	}
	return nil
}

func replaceTagsTx(ctx context.Context, tx *sql.Tx, id string, tags []string, source string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE entry_id = ?`, id); err != nil {
		return translateErr(err)
	}
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO tags(entry_id, tag, source) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range tags {
		if _, err := stmt.ExecContext(ctx, id, t, source); err != nil {
			return translateErr(err)
		}
	}
	return nil
}

// normaliseTags lowercases, trims, deduplicates, and caps at 20 (design §12.5).
func normaliseTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 20 {
			break
		}
	}
	sort.Strings(out)
	return out
}

// sourceFromRole picks the canonical tags.source value based on who is
// writing. Human/CLI/test writes are 'human'; agent writes are 'agent';
// librarian writes are 'librarian'. The enrichment pipeline writes via
// ReplaceTags with its own source string.
func sourceFromRole(role string) string {
	switch {
	case role == "", strings.HasPrefix(role, "human"), strings.HasPrefix(role, "token:"):
		return "human"
	case strings.HasPrefix(role, "librarian"):
		return "librarian"
	default:
		return "agent"
	}
}
