package store

import (
	"context"
	"testing"
)

// sanitizeFTSQuery must turn arbitrary user text into a valid FTS5
// MATCH expression. The unit test pins the transformation; the
// integration test below proves the previously-500-ing inputs now
// run without error against a real FTS5 table.
func TestSanitizeFTSQuery(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"mask", `"mask"`},
		{"training inference mask", `"training" AND "inference" AND "mask"`},
		{"train-inference", `"train-inference"`}, // hyphen was a 500 trigger
		{"foo:bar", `"foo:bar"`},                 // column-filter syntax → literal
		{"mask AND", `"mask" AND "AND"`},         // boolean operator → literal
		{"NEAR(", `"NEAR("`},                     // function syntax → literal
		{"(mask", `"(mask"`},                     // open paren → literal
		{`"unterminated`, `"""unterminated"`},    // stray quote doubled
		{"マスク", `"マスク"`},                         // single JP token
		{"マスク 生成", `"マスク" AND "生成"`},             // spaced JP
		{"mask*", `"mask"*`},                     // prefix search preserved
		{"  ", ""},                               // whitespace only → empty
		{"", ""},                                 // empty → empty
	}
	for _, c := range cases {
		if got := sanitizeFTSQuery(c.in); got != c.want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every one of these inputs returned HTTP 500 in production because
// the raw query reached FTS5 as query syntax. After sanitization they
// must all execute without error (0+ results, never a syntax error).
func TestSearchFTSDoesNotErrorOnSpecialChars(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "train-inference mask mismatch",
		Body:  "rectangular artifacts from foo:bar style mask",
	})

	for _, q := range []string{
		"train-inference",
		"foo:bar",
		"mask AND",
		"NEAR(",
		"(mask",
		`"unterminated`,
		"mask*",
		"マスク",
		"train-inference OR (mask AND foo:bar)",
	} {
		if _, _, err := s.SearchFTS(ctx, q, EntryFilter{}); err != nil {
			t.Errorf("SearchFTS(%q) errored (was a 500 in prod): %v", q, err)
		}
	}

	// Sanity: a hyphenated query actually finds the hyphenated entry.
	res, _, err := s.SearchFTS(ctx, "train-inference", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("expected hyphenated query to match the hyphenated entry")
	}
}

// Chat search shares the same sanitizer; smoke it on a special-char
// input so the path can't regress to 500 either.
func TestSearchChatFTSSpecialChars(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.SearchChatFTS(ctx, "train-inference", 10); err != nil {
		t.Errorf("SearchChatFTS special-char errored: %v", err)
	}
	if _, err := s.SearchChatFTS(ctx, "   ", 10); err == nil {
		t.Error("expected empty-query error for whitespace-only input")
	}
}
