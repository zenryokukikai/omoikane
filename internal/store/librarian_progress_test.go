package store

import (
	"context"
	"errors"
	"testing"
)

// seedProgressEntries creates a project + n entries in created_at order
// and returns the IDs in creation order (oldest first).
func seedProgressEntries(t *testing.T, s *Store, n int) []string {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "trap",
			Title: "e", Body: "x", Status: "ACTIVE",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestNextUnprocessedEntryReturnsOldestFirst(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 3)
	ctx := context.Background()

	// Initial backlog: 3 entries, oldest first.
	got, err := s.NextUnprocessedEntry(ctx, "cataloger", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ids[0] {
		t.Errorf("expected oldest %s, got %s", ids[0], got.ID)
	}

	// Mark first one processed.
	if err := s.RecordProgress(ctx, &LibrarianProgress{
		Role: "cataloger", EntryID: ids[0], Action: "summarized",
	}); err != nil {
		t.Fatal(err)
	}

	// Now the second entry should come out next.
	got, err = s.NextUnprocessedEntry(ctx, "cataloger", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ids[1] {
		t.Errorf("expected second %s, got %s", ids[1], got.ID)
	}
}

// Each role has its own backlog: cataloger's progress doesn't hide
// entries from curator.
func TestNextUnprocessedEntryPerRoleIndependent(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 2)
	ctx := context.Background()

	_ = s.RecordProgress(ctx, &LibrarianProgress{
		Role: "cataloger", EntryID: ids[0], Action: "summarized",
	})
	// curator hasn't processed anything — should still see ids[0] first
	got, err := s.NextUnprocessedEntry(ctx, "curator", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ids[0] {
		t.Errorf("curator should still see oldest %s, got %s", ids[0], got.ID)
	}
}

func TestNextUnprocessedEntryDrained(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 2)
	ctx := context.Background()

	for _, id := range ids {
		_ = s.RecordProgress(ctx, &LibrarianProgress{
			Role: "cataloger", EntryID: id, Action: "no_action",
		})
	}
	_, err := s.NextUnprocessedEntry(ctx, "cataloger", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when drained, got %v", err)
	}
}

func TestNextUnprocessedEntryProjectFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "a", Name: "A"})
	_ = s.CreateProject(ctx, &Project{ID: "b", Name: "B"})
	idA, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "a", Type: "trap", Title: "A1", Body: "x", Status: "ACTIVE",
	})
	idB, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "b", Type: "trap", Title: "B1", Body: "x", Status: "ACTIVE",
	})
	// project=a → only idA
	got, _ := s.NextUnprocessedEntry(ctx, "cataloger", "a")
	if got.ID != idA {
		t.Errorf("project filter a: got %s", got.ID)
	}
	// project=b → only idB (and skipping idA in global ordering)
	got, _ = s.NextUnprocessedEntry(ctx, "cataloger", "b")
	if got.ID != idB {
		t.Errorf("project filter b: got %s", got.ID)
	}
}

// Cataloger's backlog must NEVER include librarian_meta. Without
// this exclusion, every cataloger tick that produces a new
// librarian_meta DRAFT adds one item to its own backlog and the
// drain never converges. (This regressed in production: the drain
// loop ran 9 ticks against a starting backlog of 111, produced 9
// summary DRAFTs, and the reported backlog was still 111 after.)
func TestNextUnprocessedEntryExcludesLibrarianMetaForCataloger(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	// Create one librarian_meta (should be invisible to cataloger).
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "librarian_meta",
		Title: "a cataloger summary", Body: "x", Status: "ACTIVE",
	})
	// And one trap that cataloger SHOULD see.
	trapID, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "real source", Body: "x", Status: "ACTIVE",
	})
	got, err := s.NextUnprocessedEntry(ctx, "cataloger", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != trapID {
		t.Errorf("cataloger should pick trap (%s), got %s", trapID, got.ID)
	}
	// And the BacklogSize must match — counter and pop must agree.
	n, _ := s.BacklogSize(ctx, "cataloger", "")
	if n != 1 {
		t.Errorf("cataloger backlog should be 1 (trap only, not librarian_meta), got %d", n)
	}
}

// Curator IS allowed to see librarian_meta in its backlog —
// promoting DRAFTs is its job. Verify the exclusion is role-scoped.
func TestNextUnprocessedEntryCuratorSeesLibrarianMeta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	metaID, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "librarian_meta",
		Title: "draft summary", Body: "x", Status: "DRAFT",
	})
	got, err := s.NextUnprocessedEntry(ctx, "curator", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != metaID {
		t.Errorf("curator should see librarian_meta DRAFTs, got %s", got.ID)
	}
}

// SUPERSEDED / ARCHIVED entries are intentionally excluded from the
// backlog — re-processing settled entries just churns.
func TestNextUnprocessedEntrySkipsSupersededAndArchived(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	// Create ACTIVE one — should be picked.
	idActive, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "active", Body: "x", Status: "ACTIVE",
	})
	// Create SUPERSEDED + ARCHIVED — should NOT be picked.
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "s", Body: "x", Status: "SUPERSEDED",
	})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "a", Body: "x", Status: "ARCHIVED",
	})
	got, err := s.NextUnprocessedEntry(ctx, "cataloger", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != idActive {
		t.Errorf("should pick ACTIVE entry, got %s", got.ID)
	}
}

func TestRecordProgressRejectsBadRole(t *testing.T) {
	s := newTestStore(t)
	err := s.RecordProgress(context.Background(), &LibrarianProgress{
		Role: "wizard", EntryID: "L-X", Action: "summarized",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestRecordProgressRejectsMissingFields(t *testing.T) {
	s := newTestStore(t)
	cases := []*LibrarianProgress{
		{Role: "cataloger", EntryID: "", Action: "summarized"},
		{Role: "cataloger", EntryID: "L-X", Action: ""},
	}
	for i, c := range cases {
		err := s.RecordProgress(context.Background(), c)
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d: want ErrInvalidInput, got %v", i, err)
		}
	}
}

func TestRecordProgressRejectsMissingEntry(t *testing.T) {
	s := newTestStore(t)
	err := s.RecordProgress(context.Background(), &LibrarianProgress{
		Role: "cataloger", EntryID: "L-PHANTOM", Action: "summarized",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestBacklogSize(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 5)
	ctx := context.Background()

	n, _ := s.BacklogSize(ctx, "cataloger", "")
	if n != 5 {
		t.Errorf("initial backlog: %d", n)
	}
	// Process 2.
	for _, id := range ids[:2] {
		_ = s.RecordProgress(ctx, &LibrarianProgress{
			Role: "cataloger", EntryID: id, Action: "summarized",
		})
	}
	n, _ = s.BacklogSize(ctx, "cataloger", "")
	if n != 3 {
		t.Errorf("after 2 processed: %d", n)
	}
	// curator's backlog is independent
	n, _ = s.BacklogSize(ctx, "curator", "")
	if n != 5 {
		t.Errorf("curator independent: %d", n)
	}
}

func TestListProgressMostRecentFirst(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 3)
	ctx := context.Background()
	for _, id := range ids {
		_ = s.RecordProgress(ctx, &LibrarianProgress{
			Role: "cataloger", EntryID: id, Action: "summarized",
		})
	}
	rows, err := s.ListProgress(ctx, "cataloger", "", 0) // default limit
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// First row (most recent) should be for the last-created entry.
	if rows[0].EntryID != ids[2] {
		t.Errorf("first row should be most recent: %s", rows[0].EntryID)
	}
}

func TestListProgressInstanceFilter(t *testing.T) {
	s := newTestStore(t)
	ids := seedProgressEntries(t, s, 3)
	ctx := context.Background()
	// instance "alpha" processes 2, instance "beta" processes 1.
	for _, id := range ids[:2] {
		_ = s.RecordProgress(ctx, &LibrarianProgress{
			Role: "cataloger", EntryID: id, InstanceID: "alpha", Action: "summarized",
		})
	}
	_ = s.RecordProgress(ctx, &LibrarianProgress{
		Role: "cataloger", EntryID: ids[2], InstanceID: "beta", Action: "summarized",
	})
	rows, _ := s.ListProgress(ctx, "cataloger", "alpha", 0)
	if len(rows) != 2 {
		t.Errorf("alpha: %d", len(rows))
	}
	rows, _ = s.ListProgress(ctx, "cataloger", "beta", 0)
	if len(rows) != 1 {
		t.Errorf("beta: %d", len(rows))
	}
}
