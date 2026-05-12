package store

import (
	"context"
	"strings"
	"testing"
)

func TestMigration003Tables(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, table := range []string{"symptoms_index", "symptoms_fts",
		"triggers_index", "triggers_fts", "tag_aliases", "trigger_rules"} {
		var n int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT 1 FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}
}

func TestReplaceSymptomsAndLookup(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.ReplaceSymptoms(ctx, id, []string{
		"rectangular artifact at inference",
		"NaN at training step 5000",
	}, "llm"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.LookupBySymptom(ctx, "rectangular artifact", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].EntryID != id {
		t.Fatalf("expected 1 hit for entry %s, got %+v", id, hits)
	}
	if hits[0].Source != "fts" {
		t.Fatalf("source=%q", hits[0].Source)
	}
	// Empty query rejected
	if _, err := s.LookupBySymptom(ctx, "  ", 5); err == nil {
		t.Fatal("expected error on empty query")
	}
}

func TestReplaceTriggersAndLookupFTS(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.ReplaceTriggers(ctx, id, []IndexedTrigger{
		{Phrase: "modify mask generation", Domain: "preprocessing"},
		{Phrase: "tune learning rate", Domain: "training"},
	}, "llm"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.LookupByTrigger(ctx, "modify mask generation logic", "preprocessing", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].EntryID != id {
		t.Fatalf("expected hit, got %+v", hits)
	}
	// Domain filter excludes mismatches.
	hits, err = s.LookupByTrigger(ctx, "modify mask generation", "training", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("domain filter ineffective: %+v", hits)
	}
}

func TestLookupByTriggerRuleLayerBeatsFTS(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	// Seed FTS layer
	_ = s.ReplaceTriggers(ctx, id, []IndexedTrigger{
		{Phrase: "modify mask generation", Domain: "preprocessing"},
	}, "llm")
	// Seed a rule that points at the same entry
	if err := s.UpsertTriggerRule(ctx, &TriggerRule{
		RuleID:   "mask-mod-rule",
		Pattern:  `\bmask\s+generation\b`,
		Domain:   "preprocessing",
		EntryIDs: []string{id},
		Priority: 50,
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.LookupByTrigger(ctx, "I want to modify the mask generation step",
		"preprocessing", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "rule" {
		t.Fatalf("rule layer should win: %+v", hits)
	}
}

func TestTriggerRulesCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertTriggerRule(ctx, &TriggerRule{
		RuleID: "r1", Pattern: "foo", EntryIDs: []string{"T-X"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Upsert again with new pattern
	if err := s.UpsertTriggerRule(ctx, &TriggerRule{
		RuleID: "r1", Pattern: "bar", EntryIDs: []string{"T-Y"}, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTriggerRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Pattern != "bar" || list[0].Enabled != false {
		t.Fatalf("upsert did not update: %+v", list)
	}
	if err := s.DeleteTriggerRule(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTriggerRule(ctx, "r1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTriggerRuleValidation(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertTriggerRule(context.Background(), &TriggerRule{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestTagAliasCanonical(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertTagAlias(ctx, "masking", "mask", "tester", "synonym"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.CanonicalTag(ctx, "masking"); c != "mask" {
		t.Fatalf("canonical mismatch: %q", c)
	}
	// Non-aliased tag passes through unchanged.
	if c, _ := s.CanonicalTag(ctx, "preprocessing"); c != "preprocessing" {
		t.Fatalf("non-alias should be itself: %q", c)
	}
	aliases, err := s.ListTagAliases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].Alias != "masking" {
		t.Fatalf("list: %+v", aliases)
	}
}

func TestTagAliasValidation(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertTagAlias(context.Background(), "", "x", "", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestLookupByTagsAnyAndAll(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	a, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "A", Body: "a",
		Tags: []string{"mask", "preprocessing"},
	})
	b, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "B", Body: "b",
		Tags: []string{"mask"},
	})

	// "any" → both entries match (both have "mask")
	hits, err := s.LookupByTags(ctx, []string{"mask"}, "any", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected at least 2 hits for any, got %+v", hits)
	}
	_ = a
	_ = b

	// "all" with two tags → only entry A
	hits, err = s.LookupByTags(ctx, []string{"mask", "preprocessing"}, "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].EntryID != a {
		t.Fatalf("all-mode expected only %s, got %+v", a, hits)
	}

	// Bad mode
	if _, err := s.LookupByTags(ctx, []string{"x"}, "weird", 10); err == nil {
		t.Fatal("expected error")
	}
	// No tags
	if _, err := s.LookupByTags(ctx, nil, "any", 10); err == nil {
		t.Fatal("expected error")
	}
}

func TestLookupByTagsUsesAliases(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	id, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
		Tags: []string{"mask"},
	})
	_ = s.UpsertTagAlias(ctx, "masking", "mask", "", "")

	hits, err := s.LookupByTags(ctx, []string{"masking"}, "any", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].EntryID != id {
		t.Fatalf("alias lookup failed: %+v", hits)
	}
}

func TestNormalisePhrase(t *testing.T) {
	if got := normalisePhrase("  Foo \n bar  \t  Baz "); got != "foo bar baz" {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeDecodeEntryIDs(t *testing.T) {
	if encodeEntryIDs(nil) != "[]" {
		t.Fatal("empty should be []")
	}
	in := []string{"T-X", "D-Y"}
	out := decodeEntryIDs(encodeEntryIDs(in))
	if len(out) != 2 || out[0] != "T-X" || out[1] != "D-Y" {
		t.Fatalf("roundtrip: %v", out)
	}
	if len(decodeEntryIDs("[]")) != 0 {
		t.Fatal("[] should decode to empty")
	}
	if len(decodeEntryIDs("")) != 0 {
		t.Fatal("empty should decode to empty")
	}
}

func TestFTSTokeniseEmpty(t *testing.T) {
	if ftsTokenise("   ") != "" {
		t.Fatal("whitespace-only should produce empty")
	}
	got := ftsTokenise("foo bar")
	if !strings.Contains(got, `"foo"*`) || !strings.Contains(got, `"bar"*`) {
		t.Fatalf("got %q", got)
	}
}

func TestMatchRuleBadPatternSilentlyDisabled(t *testing.T) {
	r := &TriggerRule{Pattern: "(invalid"}
	if matchRule(r, "anything") {
		t.Fatal("invalid regex must not match")
	}
}

func TestDedupeKeepBestHitOrders(t *testing.T) {
	hits := []*LookupHit{
		{EntryID: "A", Score: 1.0},
		{EntryID: "B", Score: 5.0},
		{EntryID: "A", Score: 3.0}, // higher score for A
		{EntryID: "C", Score: 2.0},
	}
	out := dedupeKeepBestHit(hits, 5)
	if len(out) != 3 {
		t.Fatalf("dedupe expected 3, got %d", len(out))
	}
	if out[0].EntryID != "B" {
		t.Fatalf("expected B first, got %s", out[0].EntryID)
	}
	// Limit
	out = dedupeKeepBestHit(hits, 2)
	if len(out) != 2 {
		t.Fatalf("limit expected 2, got %d", len(out))
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt")
	}
}

func TestDefaultIfEmpty(t *testing.T) {
	if defaultIfEmpty("", "x") != "x" || defaultIfEmpty("y", "x") != "y" {
		t.Fatal("defaultIfEmpty")
	}
}

func TestLookupByTriggerEmptyQuery(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.LookupByTrigger(context.Background(), "  ", "", 5); err == nil {
		t.Fatal("expected error")
	}
}
