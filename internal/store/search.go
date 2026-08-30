package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sanitizeFTSQuery turns a user's free-text search into a safe FTS5
// MATCH expression. The search API is a keyword search, NOT an exposed
// FTS5 query language: callers type things like "train-inference",
// `foo:bar`, "mask AND", or `"unterminated` and expect a search, not a
// 500. Passing those straight to MATCH makes FTS5 parse them as query
// syntax (column filters, boolean operators, phrase quotes, NEAR(...))
// and raise a syntax error.
//
// We defuse that by splitting on whitespace and wrapping every token as
// a quoted FTS5 string literal (doubling any embedded quote). A quoted
// token is matched literally — `-`, `:`, `(`, `AND`, etc. lose their
// special meaning. Tokens are AND-ed (all must appear), matching the
// previous default semantics for multi-word queries. A trailing `*` on
// a bare token is preserved as a prefix match, since prefix search is a
// useful and safe capability to keep.
//
// Returns "" when the input has no usable tokens; callers treat that as
// ErrInvalidInput (same as an empty query).
func sanitizeFTSQuery(q string) string {
	return sanitizeFTSQueryJoin(q, " AND ")
}

// sanitizeFTSQueryJoin is sanitizeFTSQuery with the connective as a
// parameter. The token quoting/escaping contract lives here ONCE; the
// AND→OR zero-hit fallback (issue #138) reuses it with " OR " rather
// than growing a second, drifting copy of the escaping rules.
func sanitizeFTSQueryJoin(q, connective string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		// Preserve an intentional trailing-* prefix search on an
		// otherwise-simple token (letters/digits). Anything fancier is
		// treated as a literal phrase.
		prefix := ""
		core := f
		if strings.HasSuffix(f, "*") {
			stem := strings.TrimSuffix(f, "*")
			if stem != "" && !strings.ContainsAny(stem, `"`) {
				core = stem
				prefix = "*"
			}
		}
		// Quote as an FTS5 string, doubling embedded quotes.
		escaped := strings.ReplaceAll(core, `"`, `""`)
		tokens = append(tokens, `"`+escaped+`"`+prefix)
	}
	return strings.Join(tokens, connective)
}

// Snippet markers (issue #138). The excerpt returned with every search
// hit wraps the matched span in « » rather than <mark>: the primary
// consumer is an LLM agent, and HTML tags there are both parse noise and
// an injection surface. `**` was rejected too — it collides with
// markdown bold / [[wiki-link]] syntax and breaks on odd counts, while
// FTS5 always emits these markers in pairs. The dashboard converts them
// to <mark> AFTER HTML-escaping, so entry bodies cannot inject tags.
const (
	SnippetOpen     = "«"
	SnippetClose    = "»"
	SnippetEllipsis = "…"
)

// Search match modes, reported so a caller can tell a strict hit from a
// widened one (issue #138). MatchAnd is the normal path (every long
// token must appear); MatchOr means the AND query found nothing and the
// search was retried once with the long tokens OR-ed.
const (
	MatchAnd = "and"
	MatchOr  = "or"
)

// SearchResult is an entry paired with its FTS relevance score. Score is the
// negation of SQLite's bm25() so larger == more relevant from the caller's
// perspective.
//
// Snippet is a short excerpt around the match, with the matched span
// wrapped in SnippetOpen/SnippetClose. It is ALWAYS populated — the FTS
// path gets it from SQLite's snippet(), the short-token-only path (where
// entries_fts is not even in the FROM clause) builds it in Go from the
// already-fetched Entry. Callers must not branch on which produced it.
type SearchResult struct {
	Entry   *Entry  `json:"entry"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
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
	long, short := splitFTSTokens(q)
	if len(long) == 0 && len(short) == 0 {
		return nil, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	useFTS := len(long) > 0
	conds := []string{}
	args := []any{}
	fromSQL := `FROM librarian_chat m
		JOIN librarian_chat_fts f ON f.rowid = m.rowid`
	scoreSQL := "-bm25(librarian_chat_fts)"
	if useFTS {
		conds = append(conds, "librarian_chat_fts MATCH ?")
		args = append(args, sanitizeFTSQuery(strings.Join(long, " ")))
	} else {
		// Short tokens only — trigram cannot index them; LIKE-scan the
		// (small) chat table instead, newest first.
		fromSQL = `FROM librarian_chat m`
		scoreSQL = "0"
	}
	for _, tok := range short {
		conds = append(conds, `m.content LIKE ? ESCAPE '\'`)
		args = append(args, likePattern(tok))
	}
	// Restricted views (slice 4) search only their OWN talk threads plus
	// the shared non-talk (librarian coordination) chat. The LEFT JOIN
	// keeps thread-less messages: talkThreadCond stays true when the
	// joined row is absent (NULL intent != 'talk').
	if cond, condArgs := talkThreadCond(ctx, "th"); cond != "" {
		fromSQL += `
			LEFT JOIN chat_threads th ON th.thread_id = m.thread_id`
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	orderSQL := "score DESC"
	if !useFTS {
		orderSQL = "m.timestamp DESC"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.thread_id,''), m.timestamp, m.author_role,
		       COALESCE(m.author_instance_id,''), COALESCE(m.author_user_id,''),
		       COALESCE(m.reply_to,''), COALESCE(m.mentions,''),
		       COALESCE(m.intent,''), m.content, COALESCE(m.related_entries,''),
		       m.input_tokens, m.output_tokens, COALESCE(m.metadata,''),
		       `+scoreSQL+` AS score
		`+fromSQL+`
		WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY `+orderSQL+`
		LIMIT ?`, args...)
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
// splitFTSTokens separates search terms into trigram-indexable tokens
// (>=3 runes; the trailing-* prefix form rides along) and short tokens
// (1-2 runes) that the trigram tokenizer cannot index at all — those
// become LIKE filters instead of silently matching nothing.
func splitFTSTokens(q string) (long, short []string) {
	for _, f := range strings.Fields(q) {
		// Queries are keywords, not FTS syntax: quotes are noise, and
		// the old trailing-* prefix operator is subsumed by trigram's
		// substring matching (under trigram both would otherwise be
		// matched as literal characters and kill recall).
		core := strings.ReplaceAll(strings.TrimSuffix(f, "*"), `"`, "")
		if core == "" {
			continue
		}
		if len([]rune(core)) >= 3 {
			long = append(long, core)
		} else {
			short = append(short, core)
		}
	}
	return
}

// likePattern wraps a token for a substring LIKE with escaping.
func likePattern(tok string) string {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(tok)
	return "%" + esc + "%"
}

// entriesTextBlob is the concatenation LIKE fallbacks scan — the same
// fields entries_fts indexes.
const entriesTextBlob = `(e.title || ' ' || COALESCE(e.symptom,'') || ' ' ||
	COALESCE(e.root_cause,'') || ' ' || COALESCE(e.resolution,'') || ' ' ||
	COALESCE(e.attempted_approaches,'') || ' ' || COALESCE(e.observed_behavior,'') || ' ' ||
	COALESCE(e.hypotheses,'') || ' ' || COALESCE(e.body,''))`

// SearchFTS returns the hits, the total match count, and the match mode
// (MatchAnd / MatchOr) describing how the query's tokens were combined.
//
// Multi-word natural-language queries used to dead-end: every long token
// was AND-ed, so "一週間 頑張った" returned a bare count:0 even when each
// word appeared in the corpus. When the strict AND query finds NOTHING
// and there are at least two tokens to loosen, the search is retried
// ONCE with the tokens OR-ed and reports MatchOr. A query that already
// has hits is never widened, so ranking cannot be diluted; bm25 ordering
// plus the LIMIT bound what the widened query can surface, which is why
// no extra score floor is needed (issue #138).
func (s *Store) SearchFTS(ctx context.Context, q string, f EntryFilter) ([]*SearchResult, int, string, error) {
	long, short := splitFTSTokens(q)
	if len(long) == 0 && len(short) == 0 {
		return nil, 0, "", fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	res, total, err := s.searchEntries(ctx, long, short, f, false)
	if err != nil {
		return nil, 0, "", err
	}
	if total == 0 && orRetryApplies(long, short) && s.orRetryWorthIt(ctx, f) {
		res, total, err = s.searchEntries(ctx, long, short, f, true)
		if err != nil {
			return nil, 0, "", err
		}
		return res, total, MatchOr, nil
	}
	return res, total, MatchAnd, nil
}

// orRetryWorthIt reports whether the OR retry can possibly return rows.
// A zero can come from the TOKENS (what the retry loosens) or from the
// FILTERS (which the retry does not touch): a project/type/status/tag
// that matches nothing yields zero no matter how the tokens are joined,
// so widening the trigram MATCH there is pure waste — and OR over
// trigrams broadens the candidate set enough that the wasted COUNT(*)
// is real on a large corpus. One cheap filters-only COUNT tells the two
// apart. Unfiltered queries skip the probe entirely (nothing to rule out).
func (s *Store) orRetryWorthIt(ctx context.Context, f EntryFilter) bool {
	if f.ProjectID == "" && f.Type == "" && f.Status == "" && f.Tag == "" {
		return true
	}
	probe := EntryFilter{
		ProjectID:         f.ProjectID,
		Type:              f.Type,
		Status:            f.Status,
		Tag:               f.Tag,
		IncludeSuperseded: f.IncludeSuperseded,
		Limit:             1,
	}
	_, total, err := s.ListEntries(ctx, probe)
	if err != nil {
		// The probe is an optimisation, not a gate: on error fall back to
		// retrying, so a transient failure never turns into a lost result.
		return true
	}
	return total > 0
}

// orRetryApplies reports whether a zero-hit query has anything to loosen.
// Only the tokens that actually drive the strict AND are counted: on the
// FTS path that is the long tokens (the short-token LIKEs stay AND-ed —
// they are a cheap filter, and OR-ing them invites a full scan), and on
// the short-token-only path it is the short tokens themselves.
func orRetryApplies(long, short []string) bool {
	if len(long) > 0 {
		return len(long) >= 2
	}
	return len(short) >= 2
}

// searchEntries runs one pass of the entry search. useOR selects the
// connective for the tokens that carry the query (see orRetryApplies);
// it is set only by SearchFTS's zero-hit retry.
func (s *Store) searchEntries(ctx context.Context, long, short []string, f EntryFilter, useOR bool) ([]*SearchResult, int, error) {
	useFTS := len(long) > 0
	conds := []string{}
	args := []any{}
	if useFTS {
		connective := " AND "
		if useOR {
			connective = " OR "
		}
		conds = append(conds, "entries_fts MATCH ?")
		args = append(args, sanitizeFTSQueryJoin(strings.Join(long, " "), connective))
	}
	if !useFTS && useOR && len(short) > 1 {
		// Short-token-only retry: one parenthesised OR group, so the
		// outer " AND " join (visibility, filters) still applies.
		likes := make([]string, 0, len(short))
		for _, tok := range short {
			likes = append(likes, entriesTextBlob+` LIKE ? ESCAPE '\'`)
			args = append(args, likePattern(tok))
		}
		conds = append(conds, "("+strings.Join(likes, " OR ")+")")
	} else {
		for _, tok := range short {
			conds = append(conds, entriesTextBlob+` LIKE ? ESCAPE '\'`)
			args = append(args, likePattern(tok))
		}
	}
	// Visibility narrows the FTS CANDIDATE set (design v1: never filter
	// after ranking — the total/count must reflect the caller's view).
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
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
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}

	// Short-token-only queries scan entries directly (trigram cannot
	// index 1-2 rune tokens); relevance rank is meaningless there, so
	// newest-first stands in.
	fromSQL := `FROM entries_fts
		JOIN entries e ON e.rowid = entries_fts.rowid `
	rankSQL := "bm25(entries_fts)"
	orderSQL := "rank ASC"
	// snippet() is an FTS5 function: it needs entries_fts in the FROM
	// clause AND a MATCH to know what to highlight. The short-token-only
	// path has neither, so it selects an empty snippet and the excerpt is
	// built in Go below from the Entry we already fetched (no extra
	// query). Column -1 lets FTS5 pick the column that actually matched,
	// so the excerpt comes from the hit and not from the head of a column
	// that never matched.
	//
	// The token budget is 64 — FTS5's maximum, not a tuning choice.
	// entries_fts is tokenized as TRIGRAM (migration 027), so a "token"
	// here is three characters advancing one at a time, NOT a word: 16
	// tokens would yield an 18-character excerpt, far too little to judge
	// relevance by. 64 buys the widest window FTS5 will give (~66 chars),
	// which excerptWindow then mirrors on the Go path.
	snippetSQL := `snippet(entries_fts, -1, '` + SnippetOpen + `', '` +
		SnippetClose + `', '` + SnippetEllipsis + `', 64)`
	if !useFTS {
		fromSQL = `FROM entries e `
		rankSQL = "0"
		orderSQL = "e.updated_at DESC"
		snippetSQL = "''"
	}

	countSQL := `SELECT COUNT(*) ` + fromSQL + tagJoin + `
		WHERE ` + strings.Join(conds, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	sqlStr := `SELECT ` + entryColumnsSQL + `, ` + rankSQL + ` AS rank, ` +
		snippetSQL + ` AS snippet
		` + fromSQL + tagJoin + `
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ` + orderSQL + `
		LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, 0, err
	}
	results, err := mapRows[SearchResult](rows, func(c rowScanner, r *SearchResult) error {
		e, rank, snip, err := scanEntryWithRank(c)
		if err != nil {
			return err
		}
		r.Entry = e
		r.Score = -rank
		r.Snippet = snip
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	// Every hit carries a snippet. The short-token path always lands
	// here; the FTS path only when snippet() came back empty (a match
	// confined to an UNINDEXED column, say), and the caller must not
	// have to tell the two apart.
	tokens := append(append([]string{}, long...), short...)
	for i := range results {
		if results[i].Snippet == "" {
			results[i].Snippet = entryExcerpt(results[i].Entry, tokens)
		}
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

// scanEntryWithRank scans one entryColumnsSQL row (see entry_scan.go —
// column order is a contract) plus the trailing rank and snippet columns.
func scanEntryWithRank(r scanner) (*Entry, float64, string, error) {
	var (
		e            Entry
		validTo      nullTimeBox
		enrichmentAt nullTimeBox
		rank         float64
		snippet      string
		// Scope/Metadata: empty TEXT → nil RawMessage; see scanEntry
		// in entries.go for rationale.
		scopeRaw string
		metaRaw  string
	)
	err := r.Scan(&e.ID, &e.ProjectID, &e.Type, &e.Title, &e.Status,
		&e.Symptom, &e.RootCause, &e.Resolution, &e.Prohibited,
		&e.AttemptedApproaches, &e.ObservedBehavior, &e.Hypotheses,
		&e.Body, &e.BodyFormat,
		&scopeRaw, &metaRaw,
		&e.ValidFrom, &validTo,
		&e.SupersededBy, &e.InvalidationReason,
		&e.EnrichmentVersion, &enrichmentAt,
		&e.CreatedAt, &e.UpdatedAt,
		&e.CreatedBy, &e.CreatedByRole,
		&e.Version, &e.SpaceID, &rank, &snippet)
	if err != nil {
		return nil, 0, "", translateErr(err)
	}
	if scopeRaw != "" {
		e.Scope = json.RawMessage(scopeRaw)
	}
	if metaRaw != "" {
		e.Metadata = json.RawMessage(metaRaw)
	}
	if validTo.Valid {
		t := validTo.Time
		e.ValidTo = &t
	}
	if enrichmentAt.Valid {
		t := enrichmentAt.Time
		e.EnrichmentAt = &t
	}
	return &e, rank, snippet, nil
}

// excerptWindow is roughly how many runes of context an in-Go excerpt
// shows around a match — about half of it on each side. Sized to match
// what snippet(..., 64) produces on the trigram-tokenized FTS path
// (64 trigrams ≈ 66 characters), so a reader cannot tell which path
// built the excerpt.
const excerptWindow = 66

// entryText concatenates the same fields entries_fts indexes, in the
// same order as entriesTextBlob. The in-Go excerpt searches this so it
// looks at exactly the text the LIKE filter matched against.
func entryText(e *Entry) string {
	return strings.Join([]string{
		e.Title, e.Symptom, e.RootCause, e.Resolution,
		e.AttemptedApproaches, e.ObservedBehavior, e.Hypotheses, e.Body,
	}, " ")
}

// entryExcerpt builds the snippet for a hit that SQLite's snippet()
// could not produce (see searchEntries). It centres the window on the
// first token that actually occurs in the entry and uses the same
// markers as the FTS path. When no token is found — which the LIKE
// filter makes near-impossible — it falls back to the head of the text
// so the field is never empty.
func entryExcerpt(e *Entry, tokens []string) string {
	if e == nil {
		return ""
	}
	text := strings.TrimSpace(entryText(e))
	for _, tok := range tokens {
		if start, end := foldIndex(text, tok); start >= 0 {
			return excerptAround(text, start, end)
		}
	}
	return headExcerpt(text)
}

// excerptAround cuts ~excerptWindow runes of context around [start,end)
// and wraps the matched span in the snippet markers. Truncated ends get
// the ellipsis marker, exactly as FTS5's snippet() does.
func excerptAround(text string, start, end int) string {
	side := excerptWindow / 2
	before := []rune(text[:start])
	after := []rune(text[end:])
	var b strings.Builder
	if len(before) > side {
		b.WriteString(SnippetEllipsis)
		before = before[len(before)-side:]
	}
	if head := strings.TrimSpace(string(before)); head != "" {
		b.WriteString(head)
		b.WriteString(" ")
	}
	b.WriteString(SnippetOpen)
	b.WriteString(text[start:end])
	b.WriteString(SnippetClose)
	truncated := false
	if len(after) > side {
		after = after[:side]
		truncated = true
	}
	b.WriteString(strings.TrimRight(string(after), " \t\n"))
	if truncated {
		b.WriteString(SnippetEllipsis)
	}
	return b.String()
}

// headExcerpt returns the leading excerptWindow runes of text — the
// last-resort snippet when no query token occurs in the entry at all.
func headExcerpt(text string) string {
	r := []rune(text)
	if len(r) <= excerptWindow {
		return text
	}
	return strings.TrimRight(string(r[:excerptWindow]), " \t\n") + SnippetEllipsis
}

// foldIndex reports the byte range [start,end) of the first
// case-insensitive occurrence of sub in s, or (-1,-1). It walks s
// directly rather than lowercasing both strings and calling Index:
// lowercasing can change a string's byte length, which would silently
// misalign the offset against the ORIGINAL text we slice.
func foldIndex(s, sub string) (int, int) {
	if sub == "" {
		return -1, -1
	}
	for i := range s {
		if n := foldPrefixLen(s[i:], sub); n >= 0 {
			return i, i + n
		}
	}
	return -1, -1
}

// foldPrefixLen returns the byte length of s's prefix that equals sub
// under simple case folding, or -1 when s does not start with sub.
func foldPrefixLen(s, sub string) int {
	i := 0
	for _, want := range sub {
		if i >= len(s) {
			return -1
		}
		got, size := utf8.DecodeRuneInString(s[i:])
		if unicode.ToLower(got) != unicode.ToLower(want) {
			return -1
		}
		i += size
	}
	return i
}
