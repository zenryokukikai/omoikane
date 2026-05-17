package store

import (
	"context"
	"strings"
)

// Access source constants — values that go into entry_access_log.source.
// These mirror the entry points where an agent could have seen an entry
// surfaced; the constants live here (not in package api) so the store can
// reject bad values and the api layer references one source of truth.
const (
	AccessSourceGet              = "get"
	AccessSourceSearch           = "search"
	AccessSourceLookupByTrigger  = "lookup_by_trigger"
	AccessSourceLookupBySymptom  = "lookup_by_symptom"
	AccessSourceLookupByTags     = "lookup_by_tags"
	AccessSourceLookupBySituation = "lookup_by_situation"
)

var validAccessSources = map[string]bool{
	AccessSourceGet:              true,
	AccessSourceSearch:           true,
	AccessSourceLookupByTrigger:  true,
	AccessSourceLookupBySymptom:  true,
	AccessSourceLookupByTags:     true,
	AccessSourceLookupBySituation: true,
}

// RecordAccess logs that one or more entries were surfaced to the caller.
//
// This is the "free signal" path: agents pay no cost (no field to set, no
// state to carry). It is invoked from the handlers for GET /entries/{id},
// POST /search, and POST /lookup/*. The function is fire-and-forget by
// contract — callers don't have to check the error, and the implementation
// must not block the request path on log write failures. Errors are
// returned for tests/audits but the API handlers intentionally drop them.
//
// Empty entryIDs is a valid no-op (e.g. a search that returned nothing).
// Unknown sources are rejected with ErrInvalidInput so a typo at the
// call site fails loudly during dev rather than silently producing
// un-attributable rows.
func (s *Store) RecordAccess(ctx context.Context, entryIDs []string, userID, source, query string) error {
	if !validAccessSources[source] {
		return ErrInvalidInput
	}
	if len(entryIDs) == 0 {
		return nil
	}
	// Bulk-insert in one statement to keep this O(1) DB round-trips per
	// search/lookup, not O(hits).
	placeholders := make([]string, 0, len(entryIDs))
	args := make([]any, 0, len(entryIDs)*4)
	for _, id := range entryIDs {
		placeholders = append(placeholders, "(?, ?, ?, ?)")
		// Empty query is stored as NULL via a typed nil for clarity in
		// downstream queries that filter "where query is null".
		var q any
		if query == "" {
			q = nil
		} else {
			q = query
		}
		var u any
		if userID == "" {
			u = nil
		} else {
			u = userID
		}
		args = append(args, id, u, source, q)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO entry_access_log (entry_id, user_id, source, query) VALUES `+
			strings.Join(placeholders, ", "),
		args...)
	return err
}

// ReferenceCounts returns the count of entry_access_log rows in the last
// 30 days for each requested entry ID. Missing entries map to 0 (caller
// can choose to look them up vs default).
//
// Used by entry-detail responses to expose how often an entry has been
// surfaced — a "did anyone else find this?" signal independent of explicit
// feedback.
func (s *Store) ReferenceCounts(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entry_id, COUNT(*) FROM entry_access_log
		 WHERE entry_id IN (`+strings.Join(placeholders, ",")+`)
		   AND accessed_at > datetime('now', '-30 days')
		 GROUP BY entry_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
