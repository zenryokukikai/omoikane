package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTriggerRulesYAMLMissingFile(t *testing.T) {
	s := newTestStore(t)
	// Missing file is a no-op (returns 0, nil).
	n, err := s.LoadTriggerRulesYAML(context.Background(), "/no/such/file.yaml")
	if err != nil {
		t.Fatalf("missing file should be no-op: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 loaded, got %d", n)
	}
}

func TestLoadTriggerRulesYAMLHappyPath(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "trigger_rules.yaml")
	body := `
rules:
  - rule_id: mask-mod
    pattern: '\bmask\s+generation\b'
    domain: preprocessing
    entry_ids:
      - T-001
    priority: 50
  - rule_id: tune-lr
    pattern: 'learning\s+rate'
    domain: training
    entry_ids:
      - T-002
      - T-003
    enabled: false
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := s.LoadTriggerRulesYAML(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 loaded, got %d", n)
	}
	rules, err := s.ListTriggerRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("list: %d", len(rules))
	}
	var maskMod, tuneLR *TriggerRule
	for _, r := range rules {
		if r.RuleID == "mask-mod" {
			maskMod = r
		}
		if r.RuleID == "tune-lr" {
			tuneLR = r
		}
	}
	if maskMod == nil || tuneLR == nil {
		t.Fatalf("expected both rules present: %+v", rules)
	}
	if !maskMod.Enabled || tuneLR.Enabled {
		t.Fatalf("enabled flags: mask-mod=%v tune-lr=%v", maskMod.Enabled, tuneLR.Enabled)
	}
	if len(tuneLR.EntryIDs) != 2 {
		t.Fatalf("tune-lr entry_ids: %+v", tuneLR.EntryIDs)
	}
}

func TestLoadTriggerRulesYAMLParseError(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("not: [ yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadTriggerRulesYAML(context.Background(), path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadTriggerRulesYAMLUpsertError(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-rule.yaml")
	// Missing pattern triggers ErrInvalidInput inside UpsertTriggerRule.
	body := `
rules:
  - rule_id: r1
    entry_ids: [T-X]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadTriggerRulesYAML(context.Background(), path); err == nil {
		t.Fatal("expected upsert error")
	}
}

func TestLoadTriggerRulesYAMLReadError(t *testing.T) {
	s := newTestStore(t)
	// Pass a directory path — ReadFile returns a non-NotExist error.
	dir := t.TempDir()
	if _, err := s.LoadTriggerRulesYAML(context.Background(), dir); err == nil {
		t.Fatal("expected read error")
	}
}
