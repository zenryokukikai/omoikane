package store

import (
	"context"
	"testing"
)

func TestUpdateEntryAllFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})

	str := func(s string) *string { return &s }
	tags := []string{"a", "b"}
	_, updated, err := s.UpdateEntry(ctx, id, EntryPatch{
		Title:               str("T"),
		Status:              str("ACTIVE"),
		Symptom:             str("S"),
		RootCause:           str("R"),
		Resolution:          str("Re"),
		Prohibited:          str("P"),
		AttemptedApproaches: str("AA"),
		ObservedBehavior:    str("OB"),
		Hypotheses:          str("H"),
		Body:                str("B"),
		BodyFormat:          str("plaintext"),
		Scope:               str(`{"x":1}`),
		Metadata:            str(`{"y":2}`),
		Tags:                &tags,
		ExpectedVersion:     1,
		ChangedBy:           "tester",
		ChangedByRole:       "human",
		ChangeSummary:       "all fields",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "T" || updated.Status != "ACTIVE" || updated.Symptom != "S" {
		t.Fatalf("update did not apply: %+v", updated)
	}
	if updated.Scope != `{"x":1}` {
		t.Fatalf("scope: %q", updated.Scope)
	}
	if len(updated.Tags) != 2 {
		t.Fatalf("tags: %v", updated.Tags)
	}
}

func TestListEntriesAllFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "alpha", Body: "search-term-x",
		Tags: []string{"mask"},
	})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "decision", Title: "beta", Body: "other",
	})

	// All filters combined
	es, _, err := s.ListEntries(ctx, EntryFilter{
		ProjectID: "p", Type: "trap", Status: "DRAFT",
		Tag: "mask", Query: "search-term-x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Title != "alpha" {
		t.Fatalf("got %+v", es)
	}
}

func TestSearchScopeIncludeSuperseded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "live mask", Body: "mask leaks",
	})
	_ = s.SoftDeleteEntry(ctx, id, "tester", "human")
	// Default: archived excluded
	res, _, err := s.SearchFTS(ctx, `"mask"*`, EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("default search should exclude archived, got %d", len(res))
	}
	// IncludeSuperseded: archived returned
	res, _, err = s.SearchFTS(ctx, `"mask"*`, EntryFilter{IncludeSuperseded: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("include_superseded should return archived, got %d", len(res))
	}
}

func TestSearchTagFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "tagged mask", Body: "mask",
		Tags: []string{"mask"},
	})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "untagged mask", Body: "mask",
	})
	res, _, err := s.SearchFTS(ctx, `"mask"*`, EntryFilter{Tag: "mask"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("tag filter got %d", len(res))
	}
}

func TestSetEnrichmentUnknownEntry(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetEnrichment(context.Background(), "T-NOPE", 1); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestOpenBadPath(t *testing.T) {
	// A path that should be unwritable (non-existent directory).
	if _, err := Open(context.Background(), "/nonexistent/dir/kb.db"); err == nil {
		t.Fatal("expected error for bad path")
	}
}
