package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ruleRegexCache caches compiled regex patterns for trigger_rules. The
// patterns are static once loaded so caching is both correct and a useful
// optimisation for hot-path lookups.
var ruleRegexCache = struct {
	sync.RWMutex
	m map[string]*regexp.Regexp
}{m: map[string]*regexp.Regexp{}}

// matchRule reports whether `text` matches rule.Pattern as a Go regexp,
// case-insensitively. Compilation errors disable the rule silently — bad
// regex from a YAML / admin source should never crash lookups.
func matchRule(r *TriggerRule, text string) bool {
	ruleRegexCache.RLock()
	re, ok := ruleRegexCache.m[r.Pattern]
	ruleRegexCache.RUnlock()
	if !ok {
		compiled, err := regexp.Compile("(?i)" + r.Pattern)
		if err != nil {
			return false
		}
		ruleRegexCache.Lock()
		ruleRegexCache.m[r.Pattern] = compiled
		ruleRegexCache.Unlock()
		re = compiled
	}
	return re.MatchString(text)
}

// IndexedPhrase is a row in symptoms_index or triggers_index.
type IndexedPhrase struct {
	ID         int64
	EntryID    string
	Phrase     string
	Normalized string
	Domain     string // empty for symptoms; one of preprocessing|training|... for triggers
	Source     string
	CreatedAt  time.Time
}

// TagAlias maps a non-canonical tag to its canonical form.
type TagAlias struct {
	Alias         string
	CanonicalTag  string
	CreatedAt     time.Time
	CreatedBy     string
	Notes         string
}

// TriggerRule is a deterministic rule from trigger_rules.yaml or admin API.
type TriggerRule struct {
	ID        int64
	RuleID    string
	Pattern   string
	Domain    string
	EntryIDs  []string // already JSON-decoded
	Priority  int
	Enabled   bool
	Source    string
	CreatedAt time.Time
}

// LookupHit is a single match returned by reverse-index lookups, with
// enough context for the API layer to construct a /v1/lookup/* response.
type LookupHit struct {
	EntryID  string
	Phrase   string  // the phrase that matched
	Score    float64 // higher = more relevant
	Source   string  // 'rule' | 'fts'
}

// ----------------------------------------------------------------------
// symptoms_index
// ----------------------------------------------------------------------

// ReplaceSymptoms wipes the existing symptoms for entryID and inserts the
// supplied phrases. Used by the enrichment writer after it extracts
// symptoms from an entry.
func (s *Store) ReplaceSymptoms(ctx context.Context, entryID string, phrases []string, source string) error {
	rows := make([]IndexedTrigger, len(phrases))
	for i, p := range phrases {
		rows[i] = IndexedTrigger{Phrase: p}
	}
	return s.replacePhraseIndexTx(ctx, "symptoms_index", entryID, rows, source, false)
}

// replacePhraseIndexTx wipes the indexed phrases for entryID in `table`
// (symptoms_index or triggers_index) and inserts the supplied rows inside
// one transaction. withDomain selects the triggers_index column shape;
// symptoms ignore the Domain field. Blank phrases are skipped.
func (s *Store) replacePhraseIndexTx(ctx context.Context, table, entryID string, rows []IndexedTrigger, source string, withDomain bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE entry_id = ?`, entryID); err != nil {
		return translateErr(err)
	}
	insertSQL := `
		INSERT INTO ` + table + `(entry_id, phrase, phrase_normalized, source)
		VALUES (?, ?, ?, ?)`
	if withDomain {
		insertSQL = `
		INSERT INTO ` + table + `(entry_id, phrase, phrase_normalized, domain, source)
		VALUES (?, ?, ?, ?, ?)`
	}
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		p := strings.TrimSpace(r.Phrase)
		if p == "" {
			continue
		}
		args := []any{entryID, p, normalisePhrase(p)}
		if withDomain {
			args = append(args, nullable(r.Domain))
		}
		args = append(args, source)
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return translateErr(err)
		}
	}
	return tx.Commit()
}

// LookupBySymptom searches symptoms_index via FTS5 and returns the most
// relevant entries (de-duplicated, ordered by best score). limit defaults
// to 10 when <= 0.
func (s *Store) LookupBySymptom(ctx context.Context, query string, limit int) ([]*LookupHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	limit = clampLimit(limit, 10, 100)
	// The index rows never leave entries' FK, but the visibility filter
	// needs the entries join (design v2: lookups must narrow at the
	// candidate stage — a hidden entry's indexed phrase is itself
	// content).
	hits, err := s.ftsLookup(ctx, query, limit, ftsLookupSpec{
		selectSQL: `
		SELECT s.entry_id, s.phrase, bm25(symptoms_fts) AS rank
		FROM symptoms_fts
		JOIN symptoms_index s ON s.id = symptoms_fts.rowid
		JOIN entries e ON e.id = s.entry_id
		WHERE symptoms_fts MATCH ?`,
	})
	if err != nil {
		return nil, err
	}
	if hits == nil {
		return nil, nil
	}
	return dedupeKeepBestHit(hits, limit), nil
}

// ----------------------------------------------------------------------
// triggers_index
// ----------------------------------------------------------------------

// IndexedTrigger is a (phrase, domain) pair used by ReplaceTriggers.
type IndexedTrigger struct {
	Phrase string
	Domain string
}

// ReplaceTriggers wipes the existing triggers for entryID and inserts the
// supplied (phrase, domain) pairs.
func (s *Store) ReplaceTriggers(ctx context.Context, entryID string, triggers []IndexedTrigger, source string) error {
	return s.replacePhraseIndexTx(ctx, "triggers_index", entryID, triggers, source, true)
}

// EntrySymptoms returns the symptom phrases indexed for one entry — the
// "this entry is reachable from these symptoms" view on the entry page.
func (s *Store) EntrySymptoms(ctx context.Context, entryID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT phrase FROM symptoms_index WHERE entry_id = ? ORDER BY phrase`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EntryTriggers returns the (phrase, domain) triggers indexed for one entry.
func (s *Store) EntryTriggers(ctx context.Context, entryID string) ([]IndexedTrigger, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT phrase, COALESCE(domain,'') FROM triggers_index WHERE entry_id = ? ORDER BY domain, phrase`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedTrigger
	for rows.Next() {
		var t IndexedTrigger
		if err := rows.Scan(&t.Phrase, &t.Domain); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LookupByTrigger first consults trigger_rules (deterministic regex layer)
// then falls back to the FTS index. Rule hits get a high synthetic score
// so they sort first. The `domain` filter is applied to both layers.
func (s *Store) LookupByTrigger(ctx context.Context, query, domain string, limit int) ([]*LookupHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidInput)
	}
	limit = clampLimit(limit, 10, 100)

	// --- Layer 1: rules ---
	rules, err := s.loadEnabledTriggerRules(ctx, domain)
	if err != nil {
		return nil, err
	}
	var hits []*LookupHit
	for _, r := range rules {
		if matchRule(r, query) {
			for _, eid := range r.EntryIDs {
				hits = append(hits, &LookupHit{
					EntryID: eid, Phrase: r.RuleID,
					// rules win against FTS by orders of magnitude
					Score:  1000.0 + float64(r.Priority),
					Source: "rule",
				})
			}
		}
	}
	// Rule hits carry raw entry ids with no entries join — narrow them
	// to the ctx's visible spaces before they can crowd out (or leak
	// into) the result.
	if len(hits) > 0 {
		var err error
		hits, err = s.filterVisibleHits(ctx, hits)
		if err != nil {
			return nil, err
		}
	}

	// --- Layer 2: FTS ---
	ftsHits, err := s.ftsLookup(ctx, query, limit, ftsLookupSpec{
		selectSQL: `SELECT t.entry_id, t.phrase, bm25(triggers_fts) AS rank
			FROM triggers_fts
			JOIN triggers_index t ON t.id = triggers_fts.rowid
			JOIN entries e ON e.id = t.entry_id
			WHERE triggers_fts MATCH ?`,
		domainCol: "t.domain",
		domain:    domain,
	})
	if err != nil {
		return nil, err
	}
	hits = append(hits, ftsHits...)

	return dedupeKeepBestHit(hits, limit), nil
}

func (s *Store) loadEnabledTriggerRules(ctx context.Context, domain string) ([]*TriggerRule, error) {
	var (
		args = []any{}
		sb   strings.Builder
	)
	sb.WriteString(`SELECT id, rule_id, pattern, COALESCE(domain,''),
		entry_ids, priority, enabled, source, created_at
		FROM trigger_rules
		WHERE enabled = 1`)
	if domain != "" {
		sb.WriteString(` AND (domain = ? OR domain IS NULL OR domain = '')`)
		args = append(args, domain)
	}
	sb.WriteString(` ORDER BY priority DESC, id ASC`)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[TriggerRule](rows, func(c rowScanner, r *TriggerRule) error {
		var enabled int
		var entryIDs string
		if err := c.Scan(&r.ID, &r.RuleID, &r.Pattern, &r.Domain,
			&entryIDs, &r.Priority, &enabled, &r.Source, &r.CreatedAt); err != nil {
			return err
		}
		r.Enabled = enabled != 0
		r.EntryIDs = decodeEntryIDs(entryIDs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*TriggerRule, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ----------------------------------------------------------------------
// trigger_rules CRUD (admin / yaml loader)
// ----------------------------------------------------------------------

// UpsertTriggerRule inserts a new rule or replaces the existing one with
// the same rule_id.
func (s *Store) UpsertTriggerRule(ctx context.Context, r *TriggerRule) error {
	if r.RuleID == "" || r.Pattern == "" {
		return fmt.Errorf("%w: rule_id and pattern required", ErrInvalidInput)
	}
	if r.Priority == 0 {
		r.Priority = 100
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO trigger_rules(rule_id, pattern, domain, entry_ids, priority, enabled, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(rule_id) DO UPDATE SET
		    pattern = excluded.pattern,
		    domain = excluded.domain,
		    entry_ids = excluded.entry_ids,
		    priority = excluded.priority,
		    enabled = excluded.enabled,
		    source = excluded.source`,
		r.RuleID, r.Pattern, nullable(r.Domain), encodeEntryIDs(r.EntryIDs),
		r.Priority, boolToInt(r.Enabled), defaultIfEmpty(r.Source, "yaml"))
	return translateErr(err)
}

func (s *Store) ListTriggerRules(ctx context.Context) ([]*TriggerRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rule_id, pattern, COALESCE(domain,''),
		       entry_ids, priority, enabled, source, created_at
		FROM trigger_rules
		ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[TriggerRule](rows, func(c rowScanner, r *TriggerRule) error {
		var enabled int
		var entryIDs string
		if err := c.Scan(&r.ID, &r.RuleID, &r.Pattern, &r.Domain,
			&entryIDs, &r.Priority, &enabled, &r.Source, &r.CreatedAt); err != nil {
			return err
		}
		r.Enabled = enabled != 0
		r.EntryIDs = decodeEntryIDs(entryIDs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*TriggerRule, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

func (s *Store) DeleteTriggerRule(ctx context.Context, ruleID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM trigger_rules WHERE rule_id = ?`, ruleID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ----------------------------------------------------------------------
// tag_aliases
// ----------------------------------------------------------------------

func (s *Store) UpsertTagAlias(ctx context.Context, alias, canonical, createdBy, notes string) error {
	if alias == "" || canonical == "" {
		return fmt.Errorf("%w: alias and canonical_tag required", ErrInvalidInput)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tag_aliases(alias, canonical_tag, created_by, notes)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(alias) DO UPDATE SET
		    canonical_tag = excluded.canonical_tag,
		    created_by = excluded.created_by,
		    notes = excluded.notes`,
		alias, canonical, nullable(createdBy), nullable(notes))
	return translateErr(err)
}

// CanonicalTag returns the canonical form of `tag` (the tag itself when
// not aliased).
func (s *Store) CanonicalTag(ctx context.Context, tag string) (string, error) {
	var c string
	err := s.db.QueryRowContext(ctx,
		`SELECT canonical_tag FROM tag_aliases WHERE alias = ?`, tag).Scan(&c)
	if err == sql.ErrNoRows {
		return tag, nil
	}
	if err != nil {
		return "", err
	}
	return c, nil
}

func (s *Store) ListTagAliases(ctx context.Context) ([]*TagAlias, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT alias, canonical_tag, created_at, COALESCE(created_by,''), COALESCE(notes,'')
		FROM tag_aliases ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[TagAlias](rows, func(c rowScanner, a *TagAlias) error {
		return c.Scan(&a.Alias, &a.CanonicalTag, &a.CreatedAt, &a.CreatedBy, &a.Notes)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*TagAlias, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// LookupByTags returns entries that carry the requested tags. `mode` is
// either 'all' (entries that have every tag) or 'any' (entries that have
// at least one). Tags are canonicalised against tag_aliases first.
func (s *Store) LookupByTags(ctx context.Context, tags []string, mode string, limit int) ([]*LookupHit, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("%w: tags required", ErrInvalidInput)
	}
	if mode == "" {
		mode = "any"
	}
	if mode != "any" && mode != "all" {
		return nil, fmt.Errorf("%w: match_mode must be any|all", ErrInvalidInput)
	}
	limit = clampLimit(limit, 10, 100)

	// Canonicalise + dedupe.
	canon := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		c, err := s.CanonicalTag(ctx, t)
		if err != nil {
			return nil, err
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		canon = append(canon, c)
	}
	if len(canon) == 0 {
		return nil, nil
	}

	// Build a query that counts matching tags per entry, narrowed to the
	// ctx's visible spaces (tags reference entries; a hidden entry's id
	// must not surface).
	placeholders := strings.Repeat("?,", len(canon))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(canon)+1)
	for _, c := range canon {
		args = append(args, c)
	}
	where := `t.tag IN (` + placeholders + `)`
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		where += " AND " + cond
		args = append(args, condArgs...)
	}

	var q string
	if mode == "all" {
		q = `SELECT t.entry_id, COUNT(*) AS hits
			FROM tags t
			JOIN entries e ON e.id = t.entry_id
			WHERE ` + where + `
			GROUP BY t.entry_id
			HAVING hits = ?
			ORDER BY hits DESC, t.entry_id
			LIMIT ?`
		args = append(args, len(canon), limit)
	} else {
		q = `SELECT t.entry_id, COUNT(*) AS hits
			FROM tags t
			JOIN entries e ON e.id = t.entry_id
			WHERE ` + where + `
			GROUP BY t.entry_id
			ORDER BY hits DESC, t.entry_id
			LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LookupHit](rows, func(c rowScanner, h *LookupHit) error {
		var hits int
		if err := c.Scan(&h.EntryID, &hits); err != nil {
			return err
		}
		h.Score = float64(hits)
		h.Source = "tag"
		h.Phrase = strings.Join(canon, ",")
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LookupHit, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

// clampLimit clamps a caller-supplied page size: def when <= 0, capped at
// max. Clamp explicitly — cap at the upper bound rather than silently
// dropping to the default on overflow.
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// ftsLookupSpec parameterises ftsLookup with the per-index candidate query.
type ftsLookupSpec struct {
	// selectSQL is the core candidate query — SELECT columns, joins
	// (which MUST include entries aliased `e` for visibility), and the
	// `WHERE <fts_table> MATCH ?` predicate. ftsLookup appends the space
	// visibility condition, the optional domain predicate, and
	// `ORDER BY rank ASC LIMIT ?`.
	selectSQL string
	// domainCol, when non-empty together with domain, appends
	// `AND <domainCol> = ?` after the visibility condition.
	domainCol string
	domain    string
	// scan maps one row to a LookupHit. nil selects the standard 3-column
	// shape (entry_id, phrase, rank) with Score=-rank, Source="fts".
	scan func(rowScanner, *LookupHit) error
}

// ftsLookup runs one FTS5 candidate query shared by the symptom / trigger /
// situation lookups: tokenise → bm25-ranked candidates (limit*3) narrowed
// by spaceCond("e") — never inline space SQL. It returns the raw hits
// (callers dedupe), or nil with no error when the query yields no usable
// FTS tokens.
func (s *Store) ftsLookup(ctx context.Context, query string, limit int, spec ftsLookupSpec) ([]*LookupHit, error) {
	q := ftsTokenise(query)
	if q == "" {
		return nil, nil
	}
	sqlQ := spec.selectSQL
	args := []any{q}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		sqlQ += " AND " + cond
		args = append(args, condArgs...)
	}
	if spec.domainCol != "" && spec.domain != "" {
		sqlQ += " AND " + spec.domainCol + " = ?"
		args = append(args, spec.domain)
	}
	sqlQ += `
		ORDER BY rank ASC
		LIMIT ?`
	args = append(args, limit*3)
	rows, err := s.db.QueryContext(ctx, sqlQ, args...)
	if err != nil {
		return nil, err
	}
	scan := spec.scan
	if scan == nil {
		scan = func(c rowScanner, h *LookupHit) error {
			var rank float64
			if err := c.Scan(&h.EntryID, &h.Phrase, &rank); err != nil {
				return err
			}
			h.Score = -rank
			h.Source = "fts"
			return nil
		}
	}
	values, err := mapRows[LookupHit](rows, scan)
	if err != nil {
		return nil, err
	}
	hits := make([]*LookupHit, len(values))
	for i := range values {
		hits[i] = &values[i]
	}
	return hits, nil
}

// normalisePhrase lowercases, trims, collapses whitespace. Used as the
// `phrase_normalized` column so equality lookups can short-circuit FTS.
func normalisePhrase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// ftsTokenise turns a natural-language query into a safe FTS5 MATCH
// expression. We use OR semantics so a long query (e.g. "I want to modify
// the mask generation step") can still match a short stored trigger phrase
// (e.g. "modify mask generation"). bm25 ranks rows that match more
// distinct terms higher.
func ftsTokenise(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', ';', '.', '(', ')', '[', ']', '{', '}',
			'"', '\'', '`', ':', '/', '\\', '!', '?', '=', '<', '>', '|':
			return true
		}
		return false
	})
	toks := make([]string, 0, len(fields))
	for _, f := range fields {
		// FTS5 stop-word style: skip very short tokens to avoid noise.
		if len(f) < 2 {
			continue
		}
		toks = append(toks, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(toks, " OR ")
}

// filterVisibleHits drops hits whose entry lies outside the ctx's
// visible spaces. No-op (no query) for an unrestricted ctx — hits from
// rule tables may then still reference nonexistent entries, which the
// API projection stage already tolerates.
func (s *Store) filterVisibleHits(ctx context.Context, hits []*LookupHit) ([]*LookupHit, error) {
	cond, condArgs := spaceCond(ctx, "e")
	if cond == "" {
		return hits, nil
	}
	ids := make([]any, 0, len(hits))
	ph := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.EntryID)
		ph = append(ph, "?")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id FROM entries e WHERE e.id IN (`+strings.Join(ph, ",")+`) AND `+cond,
		append(ids, condArgs...)...)
	if err != nil {
		return nil, err
	}
	visible, err := collectStrings(rows)
	if err != nil {
		return nil, err
	}
	ok := make(map[string]bool, len(visible))
	for _, id := range visible {
		ok[id] = true
	}
	out := hits[:0]
	for _, h := range hits {
		if ok[h.EntryID] {
			out = append(out, h)
		}
	}
	return out, nil
}

// dedupeKeepBestHit keeps the highest-scored hit per (entry_id) and returns
// at most `limit` results, sorted by Score DESC.
func dedupeKeepBestHit(hits []*LookupHit, limit int) []*LookupHit {
	best := map[string]*LookupHit{}
	for _, h := range hits {
		if cur, ok := best[h.EntryID]; !ok || h.Score > cur.Score {
			best[h.EntryID] = h
		}
	}
	out := make([]*LookupHit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	// Insertion sort by Score DESC — small N
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j].Score > out[j-1].Score {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func encodeEntryIDs(ids []string) string {
	// JSON-array shape, since SQLite has json1 and admins might query it
	// directly. Tiny enough to roll our own — avoids encoding/json import
	// just for this.
	if len(ids) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(id, `"`, `\"`))
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}

func decodeEntryIDs(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultIfEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
