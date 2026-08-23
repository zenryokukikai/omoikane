package store

import (
	"context"
	"testing"
	"time"
)

// Fault aspect: input validation failures and clean-path branch coverage —
// no injected DB or environment faults. The harness (seed) lives in
// fault_test.go.

func TestGetEntryAsOfBeforeCreation(t *testing.T) {
	s, id := seed(t)
	// Far-past timestamp → no matching history row → ErrNotFound.
	if _, err := s.GetEntryAsOf(context.Background(), id, time.Unix(0, 0).UTC()); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSearchFTSTagFilterCleanRun(t *testing.T) {
	// Sanity: a search filtered by a tag with no entries returns empty
	// without error. Exercises the tag-join path and the empty-result
	// scan loop.
	s, _ := seed(t)
	if _, _, err := s.SearchFTS(context.Background(), `"x"*`, EntryFilter{
		Tag: "no-such-tag",
	}); err != nil {
		t.Fatalf("clean search should work: %v", err)
	}
}

// ---- validation paths ----

func TestCreateEntryValidationFailures(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	cases := map[string]Entry{
		"missing project_id": {ProjectID: "", Type: "trap", Title: "t", Body: "b"},
		"missing title":      {ProjectID: "p", Type: "trap", Title: "", Body: "b"},
		"missing body":       {ProjectID: "p", Type: "trap", Title: "t", Body: ""},
		"bogus status":       {ProjectID: "p", Type: "trap", Title: "t", Body: "b", Status: "BOGUS"},
	}
	for name, e := range cases {
		e := e
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateEntry(ctx, &e); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCreateEntryBodyFormatDefault(t *testing.T) {
	s, _ := seed(t)
	// Empty BodyFormat should fall back to "markdown".
	id, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEntry(context.Background(), id)
	if got.BodyFormat != "markdown" {
		t.Fatalf("BodyFormat=%q", got.BodyFormat)
	}
}

func TestCreateEntryWritesIntoStore(t *testing.T) {
	s, _ := seed(t)
	// Pre-set ID skips the random retry loop entirely.
	id, err := s.CreateEntry(context.Background(), &Entry{
		ID: "T-PRESET", ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "T-PRESET" {
		t.Fatalf("ID not preserved: %q", id)
	}
}

func TestTypePrefixDefault(t *testing.T) {
	if got := typePrefix("totally-bogus"); got != "E" {
		t.Fatalf("typePrefix(unknown) = %q, want E", got)
	}
}

// ---- CreateProject validation ----

func TestCreateProjectMissingFields(t *testing.T) {
	s, _ := seed(t)
	if err := s.CreateProject(context.Background(), &Project{ID: ""}); err != ErrInvalidInput {
		t.Fatalf("empty ID: %v", err)
	}
	if err := s.CreateProject(context.Background(), &Project{ID: "x", Name: ""}); err != ErrInvalidInput {
		t.Fatalf("empty name: %v", err)
	}
}

// ---- HasScope edge cases ----

func TestHasScopeAllBranches(t *testing.T) {
	if !HasScope([]string{"read", "write"}, "write") {
		t.Fatal("required matched")
	}
	if !HasScope([]string{"read", "admin"}, "anything") {
		t.Fatal("admin wildcard")
	}
	if HasScope([]string{"read"}, "write") {
		t.Fatal("no match")
	}
	if HasScope(nil, "read") {
		t.Fatal("empty have")
	}
	if HasScope([]string{}, "read") {
		t.Fatal("empty slice")
	}
}

// ---- nullable empty string helper ----

func TestNullableEmptyAndNonEmpty(t *testing.T) {
	if v := nullable(""); v == "" { // empty returns sql.NullString{}
		t.Fatal("empty should produce typed NULL")
	}
	if v := nullable("x"); v != "x" {
		t.Fatalf("non-empty should pass through: %v", v)
	}
}

// ---- splitScopes empty ----

func TestSplitScopesEmpty(t *testing.T) {
	if s := splitScopes(""); len(s) != 0 {
		t.Fatalf("got %v", s)
	}
}

// ---- joinScopes empty / whitespace ----

func TestJoinScopesFiltersEmpty(t *testing.T) {
	got := joinScopes([]string{"", "  ", "read", " write ", "read"})
	if got != "read,write" {
		t.Fatalf("got %q", got)
	}
}

// ---- entry history after soft-delete (valid_to populated) ----

func TestEntryHistoryAfterSoftDelete(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "tester", "human"); err != nil {
		t.Fatal(err)
	}
	hist, err := s.EntryHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// One of the history versions has valid_to set; exercises the
	// `validTo.Valid` true branch in the EntryHistory scanner.
	gotValidTo := false
	for _, h := range hist {
		if h.ValidTo != nil {
			gotValidTo = true
		}
	}
	if !gotValidTo {
		t.Fatal("expected at least one history row with valid_to set")
	}
}

func TestSearchFTSWithTypeAndStatus(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "decision", Title: "for-search", Body: "extra",
		Status: "ACTIVE",
	})
	res, _, err := s.SearchFTS(ctx, `"for-search"*`, EntryFilter{
		Type: "decision", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
}

func TestSearchFTSEntryWithEnrichmentAt(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SetEnrichment(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.SearchFTS(ctx, `"y"*`, EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, r := range res {
		if r.Entry.EnrichmentAt != nil {
			hit = true
		}
	}
	if !hit {
		t.Fatal("expected enrichment_at on at least one result")
	}
}

func TestSearchFTSEntryWithValidTo(t *testing.T) {
	// Archived entries that we include via IncludeSuperseded have
	// valid_to populated, hitting the corresponding branch in
	// scanEntryWithRank.
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.SearchFTS(ctx, `"y"*`, EntryFilter{IncludeSuperseded: true})
	if err != nil {
		t.Fatal(err)
	}
	gotValidTo := false
	for _, r := range res {
		if r.Entry.ValidTo != nil {
			gotValidTo = true
		}
	}
	if !gotValidTo {
		t.Fatal("expected at least one result with valid_to set")
	}
}

func TestGetEntryAsOfAfterSoftDelete(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEntryAsOf(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidTo == nil {
		t.Fatal("expected valid_to on archived snapshot")
	}
}

// ---- ListEntries query-fails-after-count-succeeds (unreachable in
// practice; covered by closed-store shotgun which fails count first) ----
