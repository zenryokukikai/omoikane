package store

import (
	"context"
	"errors"
	"testing"
)

func TestRecordAccessHappyPath(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	ctx := context.Background()
	// Record 3 access events on this entry.
	for i := 0; i < 3; i++ {
		if err := s.RecordAccess(ctx, []string{id}, "u1",
			AccessSourceSearch, "narwhal"); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	counts, err := s.ReferenceCounts(ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if counts[id] != 3 {
		t.Errorf("reference count: %d", counts[id])
	}
}

func TestRecordAccessEmptyIDsIsNoOp(t *testing.T) {
	s := newTestStore(t)
	if err := s.RecordAccess(context.Background(), nil, "u1",
		AccessSourceSearch, ""); err != nil {
		t.Fatalf("nil IDs should be no-op, got %v", err)
	}
}

// An unknown source must error — typos at the call site should fail loud
// during dev, not silently store un-attributable rows.
func TestRecordAccessRejectsBadSource(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	err := s.RecordAccess(context.Background(), []string{id}, "u1",
		"telepathy", "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestReferenceCountsBulk(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	a, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "A", Body: "a", Status: "ACTIVE",
	})
	b, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "B", Body: "b", Status: "ACTIVE",
	})
	c, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "C", Body: "c", Status: "ACTIVE",
	})
	// a: 5 hits, b: 2 hits, c: 0 hits.
	for i := 0; i < 5; i++ {
		_ = s.RecordAccess(ctx, []string{a}, "u1", AccessSourceGet, "")
	}
	for i := 0; i < 2; i++ {
		_ = s.RecordAccess(ctx, []string{b}, "u1", AccessSourceGet, "")
	}
	counts, err := s.ReferenceCounts(ctx, []string{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if counts[a] != 5 {
		t.Errorf("a: %d", counts[a])
	}
	if counts[b] != 2 {
		t.Errorf("b: %d", counts[b])
	}
	if _, present := counts[c]; present {
		t.Errorf("c had no accesses but appears in map: %d", counts[c])
	}
}

// Bulk insert: passing multiple IDs in one call records one row per ID.
// This is the path used by /v1/search and /v1/lookup/* where a single
// query can surface many entries.
func TestRecordAccessBulkInsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	a, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "A", Body: "a", Status: "ACTIVE",
	})
	b, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "B", Body: "b", Status: "ACTIVE",
	})
	if err := s.RecordAccess(ctx, []string{a, b}, "u1",
		AccessSourceSearch, "matched"); err != nil {
		t.Fatal(err)
	}
	counts, _ := s.ReferenceCounts(ctx, []string{a, b})
	if counts[a] != 1 || counts[b] != 1 {
		t.Errorf("counts: %v", counts)
	}
}

func TestReferenceCountsEmptyInput(t *testing.T) {
	s := newTestStore(t)
	got, err := s.ReferenceCounts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}
