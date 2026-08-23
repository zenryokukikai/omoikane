// This file is the package-wide reverse-lookup layer: the Lookup* entry
// points over the symptom / trigger / tag indexes, the trigger rule
// engine, and the FTS candidate-query helpers. situations.go's
// LookupBySituation consumes ftsLookup / clampLimit / dedupeKeepBestHit
// from here too. The write side of the indexes (Replace* etc.) lives in
// reverse_index.go.

package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
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
// lookup helpers
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
