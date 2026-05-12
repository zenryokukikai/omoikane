package enrich

import (
	"context"
	"strings"
	"testing"
)

func TestHeuristicHitsExpectedKeywords(t *testing.T) {
	h := heuristic{}
	r, err := h.Enrich(context.Background(), Input{
		Title:   "Train-Inference Mask Mismatch",
		Body:    "training pipeline used a rectangular mask but inference used a soft mask; artifact appeared",
		Symptom: "rectangular artifact at inference time",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "heuristic" {
		t.Fatalf("source=%q", r.Source)
	}
	if len(r.Tags) == 0 {
		t.Fatal("expected tags")
	}
	top3 := r.Tags
	if len(top3) > 3 {
		top3 = top3[:3]
	}
	found := false
	for _, tag := range top3 {
		if tag == "mask" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'mask' in top 3, got %v", r.Tags)
	}
}

func TestStopWordsAreDropped(t *testing.T) {
	r, _ := heuristic{}.Enrich(context.Background(), Input{
		Title: "the and for with this that not have",
		Body:  "the and for with this that not have",
	})
	if len(r.Tags) != 0 {
		t.Fatalf("expected no tags, got %v", r.Tags)
	}
}

func TestNewFallbackToHeuristic(t *testing.T) {
	if New("anthropic", "", "", "", nil).Name() != "heuristic" {
		t.Fatal("provider stub should currently fall back to heuristic")
	}
}

func TestNewExplicitHeuristic(t *testing.T) {
	for _, name := range []string{"", "heuristic", "none", "off"} {
		if got := New(name, "", "", "", nil).Name(); got != "heuristic" {
			t.Fatalf("New(%q) → %s, want heuristic", name, got)
		}
	}
}

func TestEnrichEmptyTextReturnsNoTags(t *testing.T) {
	r, err := heuristic{}.Enrich(nil, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tags) != 0 {
		t.Fatalf("empty input should have no tags, got %v", r.Tags)
	}
}

func TestExtractProhibitedPatterns(t *testing.T) {
	out := extractProhibitedPatterns(Input{
		Prohibited: "- DO NOT use rectangular mask anywhere\n" +
			"* DO NOT use cv2.rectangle on target\n" +
			"DO NOT use rectangular mask anywhere\n" + // duplicate
			"   \n" + // whitespace-only is dropped
			"x\n" +   // < 4 chars after strip
			"a third unique rule",
	})
	if len(out) != 3 {
		t.Fatalf("expected 3 unique patterns, got %d: %v", len(out), out)
	}
}

func TestExtractProhibitedPatternsEmpty(t *testing.T) {
	if got := extractProhibitedPatterns(Input{}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestDetectDomainAllBuckets(t *testing.T) {
	cases := map[string]string{
		"this is about preprocessing":          "preprocessing",
		"during training the loss exploded":    "training",
		"at inference time we observed":        "inference",
		"the data pipeline corrupted the file": "data",
		"infra outage in the datacenter":       "infra",
		"unrelated text":                       "",
	}
	for text, want := range cases {
		if got := detectDomain(text, ""); got != want {
			t.Errorf("%q → %q, want %q", text, got, want)
		}
	}
	// Explicit hint wins.
	if got := detectDomain("anything", "training"); got != "training" {
		t.Fatalf("hint should win, got %q", got)
	}
}

func TestExtractScopeFrameworksAndGPUs(t *testing.T) {
	in := Input{
		Body:             "we tested with pytorch and jax on h100 and a100",
		Symptom:          "",
		ObservedBehavior: "tensorflow comparison",
	}
	got := extractScope(in)
	if got == nil {
		t.Fatal("expected non-nil scope")
	}
	if len(got["frameworks"]) < 2 || len(got["gpus"]) < 2 {
		t.Fatalf("incomplete extraction: %v", got)
	}
}

func TestExtractScopeNoneReturnsNil(t *testing.T) {
	if got := extractScope(Input{Body: "completely unrelated text"}); got != nil {
		t.Fatalf("expected nil for no matches, got %v", got)
	}
}

func TestExtractTriggerPhrases(t *testing.T) {
	out := extractTriggerPhrases(Input{
		Title:      "Train-Inference Mask Mismatch",
		Body:       "we should modify the mask generation to be soft and tune learning rate carefully",
		Resolution: "change the masking approach",
	})
	if len(out) == 0 {
		t.Fatal("expected some triggers")
	}
	gotModify := false
	for _, t := range out {
		if strings.HasPrefix(t.Phrase, "modify") {
			gotModify = true
		}
	}
	if !gotModify {
		t.Fatalf("expected a modify-* trigger, got %+v", out)
	}
}
