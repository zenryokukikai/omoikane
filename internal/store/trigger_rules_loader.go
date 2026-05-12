package store

import (
	"context"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TriggerRuleYAML mirrors one entry in trigger_rules.yaml.
type TriggerRuleYAML struct {
	RuleID   string   `yaml:"rule_id"`
	Pattern  string   `yaml:"pattern"`
	Domain   string   `yaml:"domain,omitempty"`
	EntryIDs []string `yaml:"entry_ids"`
	Priority int      `yaml:"priority,omitempty"`
	Enabled  *bool    `yaml:"enabled,omitempty"`
}

// TriggerRuleFile is the top-level shape of trigger_rules.yaml.
type TriggerRuleFile struct {
	Rules []TriggerRuleYAML `yaml:"rules"`
}

// LoadTriggerRulesYAML reads a YAML file and upserts each rule into the
// trigger_rules table with source='yaml'. Missing file is a no-op (no
// rules configured is a valid state).
func (s *Store) LoadTriggerRulesYAML(ctx context.Context, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	var f TriggerRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	n := 0
	for _, r := range f.Rules {
		enabled := true
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		if err := s.UpsertTriggerRule(ctx, &TriggerRule{
			RuleID:   r.RuleID,
			Pattern:  r.Pattern,
			Domain:   r.Domain,
			EntryIDs: r.EntryIDs,
			Priority: r.Priority,
			Enabled:  enabled,
			Source:   "yaml",
		}); err != nil {
			return n, fmt.Errorf("upsert rule %q: %w", r.RuleID, err)
		}
		n++
	}
	return n, nil
}
