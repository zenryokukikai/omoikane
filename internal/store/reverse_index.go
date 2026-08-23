package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

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
	Alias        string
	CanonicalTag string
	CreatedAt    time.Time
	CreatedBy    string
	Notes        string
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
	EntryID string
	Phrase  string  // the phrase that matched
	Score   float64 // higher = more relevant
	Source  string  // 'rule' | 'fts'
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

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

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
