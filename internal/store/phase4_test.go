package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func phase4Seed(t *testing.T) (*Store, context.Context, []string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 3)
	for _, title := range []string{"alpha", "beta", "gamma"} {
		id, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "trap", Title: title,
			Body: "b", Tags: []string{title, "shared"},
			Status: "ACTIVE",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return s, ctx, ids
}

// ============================================================
// hierarchy_nodes + hierarchy_entries
// ============================================================

func TestHierarchyCRUD(t *testing.T) {
	s, ctx, ids := phase4Seed(t)

	rootID, err := s.CreateHierarchyNode(ctx, &HierarchyNode{
		ProjectID: "p", Name: "root", Description: "top",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rootID, "H-") {
		t.Fatalf("expected H- prefix, got %s", rootID)
	}
	childID, err := s.CreateHierarchyNode(ctx, &HierarchyNode{
		ProjectID: "p", ParentID: rootID, Name: "child",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetHierarchyNode(ctx, rootID)
	if err != nil || got.Name != "root" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	roots, err := s.ListHierarchyNodes(ctx, "p", "")
	if err != nil || len(roots) != 1 || roots[0].ID != rootID {
		t.Fatalf("list-roots: %+v err=%v", roots, err)
	}
	children, err := s.ListHierarchyNodes(ctx, "p", rootID)
	if err != nil || len(children) != 1 || children[0].ID != childID {
		t.Fatalf("list-children: %+v err=%v", children, err)
	}

	// Attach + detach entries
	if err := s.AttachEntryToNode(ctx, rootID, ids[0], 0.9, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachEntryToNode(ctx, rootID, ids[1], 0.5, "test"); err != nil {
		t.Fatal(err)
	}
	// Idempotent
	if err := s.AttachEntryToNode(ctx, rootID, ids[1], 0.7, "test"); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListEntriesAtNode(ctx, rootID, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("list-entries: %+v err=%v", entries, err)
	}
	nodes, err := s.ListNodesForEntry(ctx, ids[0])
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes-for-entry: %+v err=%v", nodes, err)
	}

	if err := s.DetachEntryFromNode(ctx, rootID, ids[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.DetachEntryFromNode(ctx, rootID, ids[1]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Delete cascades to children + entries
	if err := s.DeleteHierarchyNode(ctx, rootID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteHierarchyNode(ctx, rootID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.GetHierarchyNode(ctx, childID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("child should cascade, got %v", err)
	}
}

func TestHierarchyValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateHierarchyNode(context.Background(), &HierarchyNode{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := s.GetHierarchyNode(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get-missing: %v", err)
	}
}

func TestListEntriesAtNodeSkipsDeleted(t *testing.T) {
	s, ctx, ids := phase4Seed(t)
	nodeID, _ := s.CreateHierarchyNode(ctx, &HierarchyNode{Name: "n"})
	for _, id := range ids {
		_ = s.AttachEntryToNode(ctx, nodeID, id, 1.0, "test")
	}
	// Hard-delete one entry directly to exercise the GetEntry-error skip
	// branch. CASCADE on hierarchy_entries removes the link too, so we
	// need to disable FK first.
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM entries WHERE id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	out, err := s.ListEntriesAtNode(ctx, nodeID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 surviving entries, got %d", len(out))
	}
}

// ============================================================
// derived_summaries
// ============================================================

func TestDerivedSummaries(t *testing.T) {
	s, ctx, _ := phase4Seed(t)
	id, err := s.PutDerivedSummary(ctx, &DerivedSummary{
		SourceType: "hierarchy_node", SourceKey: "H-test",
		Title: "T", Summary: "S", EntryCount: 3,
	})
	if err != nil || !strings.HasPrefix(id, "SM-") {
		t.Fatalf("put: id=%s err=%v", id, err)
	}
	// Replacement semantics: re-put with same (source_type, source_key)
	// returns a new ID and drops the previous.
	id2, err := s.PutDerivedSummary(ctx, &DerivedSummary{
		SourceType: "hierarchy_node", SourceKey: "H-test",
		Title: "T2", Summary: "S2", EntryCount: 4,
	})
	if err != nil || id2 == id {
		t.Fatalf("replace: id=%s prev=%s err=%v", id2, id, err)
	}
	got, err := s.GetDerivedSummary(ctx, "hierarchy_node", "H-test")
	if err != nil || got.Title != "T2" {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	list, err := s.ListDerivedSummaries(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}

	if _, err := s.GetDerivedSummary(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDerivedSummaryValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PutDerivedSummary(context.Background(), &DerivedSummary{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// ============================================================
// index aggregations
// ============================================================

func TestIndexByTag(t *testing.T) {
	s, ctx, _ := phase4Seed(t)
	buckets, err := s.IndexByTag(ctx, "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	// "shared" is on all three entries → should be the top bucket.
	if len(buckets) == 0 || buckets[0].Key != "shared" || buckets[0].Count != 3 {
		t.Fatalf("buckets: %+v", buckets)
	}
}

func TestIndexByRecent(t *testing.T) {
	s, ctx, _ := phase4Seed(t)
	buckets, err := s.IndexByRecent(ctx, "p", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) == 0 {
		t.Fatal("expected at least one bucket")
	}
}

func TestIndexByHierarchy(t *testing.T) {
	s, ctx, ids := phase4Seed(t)
	rootID, _ := s.CreateHierarchyNode(ctx, &HierarchyNode{ProjectID: "p", Name: "root"})
	_ = s.AttachEntryToNode(ctx, rootID, ids[0], 1.0, "t")
	buckets, err := s.IndexByHierarchy(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Count != 1 {
		t.Fatalf("buckets: %+v", buckets)
	}
}
