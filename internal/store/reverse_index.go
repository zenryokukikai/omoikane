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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM symptoms_index WHERE entry_id = ?`, entryID); err != nil {
		return translateErr(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO symptoms_index(entry_id, phrase, phrase_normalized, source)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range phrases {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, entryID, p, normalisePhrase(p), source); err != nil {
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
		SELECT s.entry_id, s.phrase, bm25(symptoms_fts) AS rank
		FROM symptoms_fts
		JOIN symptoms_index s ON s.id = symptoms_fts.rowid
		WHERE symptoms_fts MATCH ?
		ORDER BY rank ASC
		LIMIT ?`, q, limit*3)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LookupHit](rows, func(c rowScanner, h *LookupHit) error {
		var rank float64
		if err := c.Scan(&h.EntryID, &h.Phrase, &rank); err != nil {
			return err
		}
		h.Score = -rank
		h.Source = "fts"
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM triggers_index WHERE entry_id = ?`, entryID); err != nil {
		return translateErr(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO triggers_index(entry_id, phrase, phrase_normalized, domain, source)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, tr := range triggers {
		p := strings.TrimSpace(tr.Phrase)
		if p == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, entryID, p, normalisePhrase(p),
			nullable(tr.Domain), source); err != nil {
			return translateErr(err)
		}
	}
	return tx.Commit()
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

// IndexedEntrySummary is one entry that has reverse-index coverage. It carries
// totals plus a small sample of the actual symptom / trigger phrases so the
// /lookup browse list is scannable in human terms (the phrases ARE the human
// language; counts and titles alone aren't).
type IndexedEntrySummary struct {
	EntryID         string
	Title           string
	Type            string
	ProjectID       string
	Symptoms        int
	Triggers        int
	LastIndexed     string             // max(created_at) across symptoms+triggers
	SampleSymptoms  []string           // up to 3 most-recent symptom phrases
	SampleTriggers  []IndexedTrigger   // up to 2 most-recent triggers (with domain)
}

// ListIndexedEntries lists entries that have at least one symptom or trigger
// indexed, most-recently-indexed first, with counts. Returns the requested
// page plus the total count for pagination. project="" lists all projects.
func (s *Store) ListIndexedEntries(ctx context.Context, project string, limit, offset int) ([]*IndexedEntrySummary, int, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	where := ""
	var filter []any
	if project != "" {
		where = "WHERE e.project_id = ?"
		filter = append(filter, project)
	}

	var total int
	totalSQL := `SELECT COUNT(*) FROM (
		SELECT entry_id FROM symptoms_index
		UNION SELECT entry_id FROM triggers_index
	) idx JOIN entries e ON e.id = idx.entry_id ` + where
	if err := s.db.QueryRowContext(ctx, totalSQL, filter...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageSQL := `SELECT e.id, e.title, e.type, e.project_id,
		COALESCE(s.cnt,0), COALESCE(t.cnt,0),
		MAX(COALESCE(s.last,''), COALESCE(t.last,'')) AS last_indexed
	FROM (
		SELECT entry_id FROM symptoms_index
		UNION SELECT entry_id FROM triggers_index
	) idx
	JOIN entries e ON e.id = idx.entry_id
	LEFT JOIN (SELECT entry_id, COUNT(*) cnt, MAX(created_at) last FROM symptoms_index GROUP BY entry_id) s ON s.entry_id = e.id
	LEFT JOIN (SELECT entry_id, COUNT(*) cnt, MAX(created_at) last FROM triggers_index GROUP BY entry_id) t ON t.entry_id = e.id
	` + where + `
	ORDER BY last_indexed DESC, e.id
	LIMIT ? OFFSET ?`
	args := append(append([]any{}, filter...), limit, offset)
	rows, err := s.db.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*IndexedEntrySummary
	for rows.Next() {
		var r IndexedEntrySummary
		if err := rows.Scan(&r.EntryID, &r.Title, &r.Type, &r.ProjectID,
			&r.Symptoms, &r.Triggers, &r.LastIndexed); err != nil {
			return nil, 0, err
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Enrich each row with a few sample phrases. The 30-row page makes 60 small
	// indexed queries — cheap, and far simpler than aggregate-with-LIMIT SQL.
	for _, r := range out {
		sRows, sErr := s.db.QueryContext(ctx,
			`SELECT phrase FROM symptoms_index WHERE entry_id = ?
			 ORDER BY created_at DESC, id DESC LIMIT 3`, r.EntryID)
		if sErr == nil {
			for sRows.Next() {
				var p string
				if sRows.Scan(&p) == nil {
					r.SampleSymptoms = append(r.SampleSymptoms, p)
				}
			}
			sRows.Close()
		}
		tRows, tErr := s.db.QueryContext(ctx,
			`SELECT phrase, COALESCE(domain,'') FROM triggers_index WHERE entry_id = ?
			 ORDER BY created_at DESC, id DESC LIMIT 2`, r.EntryID)
		if tErr == nil {
			for tRows.Next() {
				var t IndexedTrigger
				if tRows.Scan(&t.Phrase, &t.Domain) == nil {
					r.SampleTriggers = append(r.SampleTriggers, t)
				}
			}
			tRows.Close()
		}
	}
	return out, total, nil
}

// LookupByTrigger first consults trigger_rules (deterministic regex layer)
// then falls back to the FTS index. Rule hits get a high synthetic score
// so they sort first. The `domain` filter is applied to both layers.
func (s *Store) LookupByTrigger(ctx context.Context, query, domain string, limit int) ([]*LookupHit, error) {
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

	// --- Layer 2: FTS ---
	q := ftsTokenise(query)
	if q != "" {
		var (
			rows *sql.Rows
			args = []any{q}
			sb   strings.Builder
		)
		sb.WriteString(`SELECT t.entry_id, t.phrase, bm25(triggers_fts) AS rank
			FROM triggers_fts
			JOIN triggers_index t ON t.id = triggers_fts.rowid
			WHERE triggers_fts MATCH ?`)
		if domain != "" {
			sb.WriteString(` AND t.domain = ?`)
			args = append(args, domain)
		}
		sb.WriteString(` ORDER BY rank ASC LIMIT ?`)
		args = append(args, limit*3)
		rows, err = s.db.QueryContext(ctx, sb.String(), args...)
		if err != nil {
			return nil, err
		}
		ftsHits, err := mapRows[LookupHit](rows, func(c rowScanner, h *LookupHit) error {
			var rank float64
			if err := c.Scan(&h.EntryID, &h.Phrase, &rank); err != nil {
				return err
			}
			h.Score = -rank
			h.Source = "fts"
			return nil
		})
		if err != nil {
			return nil, err
		}
		for i := range ftsHits {
			hits = append(hits, &ftsHits[i])
		}
	}

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
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 10
	} else if limit > 100 {
		limit = 100
	}

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

	// Build a query that counts matching tags per entry.
	placeholders := strings.Repeat("?,", len(canon))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(canon)+1)
	for _, c := range canon {
		args = append(args, c)
	}

	var q string
	if mode == "all" {
		q = `SELECT t.entry_id, COUNT(*) AS hits
			FROM tags t
			WHERE t.tag IN (` + placeholders + `)
			GROUP BY t.entry_id
			HAVING hits = ?
			ORDER BY hits DESC, t.entry_id
			LIMIT ?`
		args = append(args, len(canon), limit)
	} else {
		q = `SELECT t.entry_id, COUNT(*) AS hits
			FROM tags t
			WHERE t.tag IN (` + placeholders + `)
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
