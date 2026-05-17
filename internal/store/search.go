package store

import (
	"context"
	"fmt"
	"strings"
)

// SearchResult is an entry paired with its FTS relevance score. Score is the
// negation of SQLite's bm25() so larger == more relevant from the caller's
// perspective.
type SearchResult struct {
	Entry *Entry  `json:"entry"`
	Score float64 `json:"score"`
}

// ChatSearchResult is one chat message returned by an FTS search.
// Score uses the same convention as SearchResult (larger == more
// relevant; we negate bm25). The full ChatMessage is embedded so
// callers don't need a second query to display author / thread.
type ChatSearchResult struct {
	Message *ChatMessage `json:"message"`
	Score   float64      `json:"score"`
}

// SearchChatFTS runs FTS5 against librarian_chat_fts. Chat search is
// opt-in (controlled by the API's include_chat flag) — chat is not
// searched by default because lookup-style queries want durable
// knowledge (entries), and chat traffic would dilute precision.
//
// `limit` caps the number of results; 0 means "use a sensible default"
// (50). No project / status filter yet — chat threads don't have a
// project_id, and OPEN/CLOSED filtering happens at the thread level
// (a future extension can join chat_threads and filter on status).
func (s *Store) SearchChatFTS(ctx context.Context, q string, limit int) ([]*ChatSearchResult, error) {
	if strings.TrimSpace(q) == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.thread_id,''), m.timestamp, m.author_role,
		       COALESCE(m.author_instance_id,''), COALESCE(m.author_user_id,''),
		       COALESCE(m.reply_to,''), COALESCE(m.mentions,''),
		       COALESCE(m.intent,''), m.content, COALESCE(m.related_entries,''),
		       m.input_tokens, m.output_tokens, COALESCE(m.metadata,''),
		       -bm25(librarian_chat_fts) AS score
		FROM librarian_chat m
		JOIN librarian_chat_fts f ON f.rowid = m.rowid
		WHERE librarian_chat_fts MATCH ?
		ORDER BY score DESC
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, translateErr(err)
	}
	values, err := mapRows[ChatSearchResult](rows, func(c rowScanner, r *ChatSearchResult) error {
		r.Message = &ChatMessage{}
		return c.Scan(&r.Message.ID, &r.Message.ThreadID, &r.Message.Timestamp,
			&r.Message.AuthorRole, &r.Message.AuthorInstanceID, &r.Message.AuthorUserID,
			&r.Message.ReplyTo, &r.Message.Mentions, &r.Message.Intent,
			&r.Message.Content, &r.Message.RelatedEntries,
			&r.Message.InputTokens, &r.Message.OutputTokens, &r.Message.Metadata,
			&r.Score)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*ChatSearchResult, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// SearchFTS runs FTS5 against entries_fts with optional filters and pagination.
// Returns matched entries plus total match count (for pagination).
func (s *Store) SearchFTS(ctx context.Context, q string, f EntryFilter) ([]*SearchResult, int, error) {
	if strings.TrimSpace(q) == "" {
		return nil, 0, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	conds := []string{"entries_fts MATCH ?"}
	args := []any{q}
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
	tagJoin := ""
	if f.Tag != "" {
		tagJoin = " JOIN tags t ON t.entry_id = e.id "
		conds = append(conds, "t.tag = ?")
		args = append(args, f.Tag)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// Total count first (cheap because FTS uses an index).
	countSQL := `SELECT COUNT(*) FROM entries_fts
		JOIN entries e ON e.rowid = entries_fts.rowid ` + tagJoin + `
		WHERE ` + strings.Join(conds, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	sqlStr := `SELECT ` + selectColumnsForEntry("e") + `, bm25(entries_fts) AS rank
		FROM entries_fts
		JOIN entries e ON e.rowid = entries_fts.rowid ` + tagJoin + `
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY rank ASC
		LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, 0, err
	}
	results, err := mapRows[SearchResult](rows, func(c rowScanner, r *SearchResult) error {
		e, rank, err := scanEntryWithRank(c)
		if err != nil {
			return err
		}
		r.Entry = e
		r.Score = -rank
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*SearchResult, len(results))
	entries := make([]*Entry, len(results))
	ids := make([]string, len(results))
	for i := range results {
		out[i] = &results[i]
		entries[i] = results[i].Entry
		ids[i] = results[i].Entry.ID
	}
	if err := s.attachTags(ctx, entries, ids); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// selectColumnsForEntry mirrors entrySelectSQL's columns, qualified by prefix.
// FTS5's content table shares column names with entries, so every reference
// must be qualified to disambiguate.
func selectColumnsForEntry(prefix string) string {
	plain := []string{
		"id", "project_id", "type", "title", "status",
		"body", "body_format",
		"valid_from", "enrichment_version", "created_at", "updated_at", "version",
	}
	nullableCols := []string{
		"symptom", "root_cause", "resolution", "prohibited",
		"attempted_approaches", "observed_behavior", "hypotheses",
		"scope", "metadata",
		"superseded_by", "invalidation_reason",
		"created_by", "created_by_role",
	}
	out := []string{}
	// Order MUST match entrySelectSQL exactly:
	// id, project_id, type, title, status,
	// symptom, root_cause, resolution, prohibited,
	// attempted_approaches, observed_behavior, hypotheses,
	// body, body_format,
	// scope, metadata,
	// valid_from, valid_to,
	// superseded_by, invalidation_reason,
	// enrichment_version, enrichment_at,
	// created_at, updated_at,
	// created_by, created_by_role,
	// version
	add := func(col string, isNullable bool) {
		if isNullable {
			out = append(out, "COALESCE("+prefix+"."+col+",'')")
		} else {
			out = append(out, prefix+"."+col)
		}
	}
	addRaw := func(col string) {
		out = append(out, prefix+"."+col)
	}
	_ = plain
	_ = nullableCols

	add("id", false)
	add("project_id", false)
	add("type", false)
	add("title", false)
	add("status", false)
	add("symptom", true)
	add("root_cause", true)
	add("resolution", true)
	add("prohibited", true)
	add("attempted_approaches", true)
	add("observed_behavior", true)
	add("hypotheses", true)
	add("body", false)
	add("body_format", false)
	add("scope", true)
	add("metadata", true)
	addRaw("valid_from")
	addRaw("valid_to")
	add("superseded_by", true)
	add("invalidation_reason", true)
	addRaw("enrichment_version")
	addRaw("enrichment_at")
	addRaw("created_at")
	addRaw("updated_at")
	add("created_by", true)
	add("created_by_role", true)
	addRaw("version")
	return strings.Join(out, ", ")
}

func scanEntryWithRank(r scanner) (*Entry, float64, error) {
	var (
		e            Entry
		validTo      nullTimeBox
		enrichmentAt nullTimeBox
		rank         float64
	)
	err := r.Scan(&e.ID, &e.ProjectID, &e.Type, &e.Title, &e.Status,
		&e.Symptom, &e.RootCause, &e.Resolution, &e.Prohibited,
		&e.AttemptedApproaches, &e.ObservedBehavior, &e.Hypotheses,
		&e.Body, &e.BodyFormat,
		&e.Scope, &e.Metadata,
		&e.ValidFrom, &validTo,
		&e.SupersededBy, &e.InvalidationReason,
		&e.EnrichmentVersion, &enrichmentAt,
		&e.CreatedAt, &e.UpdatedAt,
		&e.CreatedBy, &e.CreatedByRole,
		&e.Version, &rank)
	if err != nil {
		return nil, 0, translateErr(err)
	}
	if validTo.Valid {
		t := validTo.Time
		e.ValidTo = &t
	}
	if enrichmentAt.Valid {
		t := enrichmentAt.Time
		e.EnrichmentAt = &t
	}
	return &e, rank, nil
}
