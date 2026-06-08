package store

import (
	"context"
	"errors"
	"testing"
)

// TestSlugify covers the en_name → kebab-case slug derivation, including
// non-ASCII, multiple separators, and the 60-char cap.
func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Mouth articulation weak", "mouth-articulation-weak"},
		{"  Weak Open-Mouth!!  ", "weak-open-mouth"},
		{"日本語 only Title", "only-title"},
		{"a/b/c", "a-b-c"},
		{"___underscores___", "underscores"},
		{"", ""},
	}
	for _, c := range cases {
		got := Slugify(c.in)
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Length cap.
	long := Slugify("xxxxxxxxxx xxxxxxxxxx xxxxxxxxxx xxxxxxxxxx xxxxxxxxxx xxxxxxxxxx xxxxxxxxxx")
	if len(long) > 60 {
		t.Errorf("Slugify length > 60: %d (%q)", len(long), long)
	}
}

func TestUpsertUseCaseCreateAndUpdate(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()

	// Create.
	uc, err := s.UpsertUseCase(ctx, &UseCase{
		NameJA: "口の動きが弱い", NameEN: "Weak mouth articulation",
		DescriptionJA: "発話時の口の開きが小さい",
		DescriptionEN: "Mouth opens too little when speaking",
		Domain:        "lipsync",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uc.ID == "" || uc.Slug != "weak-mouth-articulation" {
		t.Fatalf("create: id=%q slug=%q", uc.ID, uc.Slug)
	}
	if uc.Source != "indexer" {
		t.Fatalf("default source: got %q", uc.Source)
	}
	firstID := uc.ID

	// Upsert with same slug (derived from same en_name) updates in place.
	uc2, err := s.UpsertUseCase(ctx, &UseCase{
		NameJA: "口の開きが弱い (revised)", NameEN: "Weak mouth articulation",
		Domain: "lipsync", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if uc2.ID != firstID {
		t.Fatalf("upsert by slug: new id %q != %q", uc2.ID, firstID)
	}
	if uc2.NameJA != "口の開きが弱い (revised)" || uc2.Source != "manual" {
		t.Fatalf("upsert didn't update: %+v", uc2)
	}

	// Missing names → ErrInvalidInput.
	if _, err := s.UpsertUseCase(ctx, &UseCase{NameEN: "only en"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing name_ja: want ErrInvalidInput, got %v", err)
	}
}

func TestLinkAndListUseCaseEntries(t *testing.T) {
	s, id1 := seed(t)
	ctx := context.Background()
	id2, err := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "lesson", Title: "B", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	uc, err := s.UpsertUseCase(ctx, &UseCase{
		NameJA: "テスト用途", NameEN: "Test usage", Domain: "lipsync",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.LinkUseCaseEntry(ctx, uc.ID, id1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkUseCaseEntry(ctx, uc.ID, id2, "test"); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-link.
	if err := s.LinkUseCaseEntry(ctx, uc.ID, id2, "test"); err != nil {
		t.Fatalf("idempotent link errored: %v", err)
	}

	// List entries on the use case.
	entries, total, err := s.ListUseCaseEntries(ctx, uc.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("entries: total=%d len=%d", total, len(entries))
	}

	// Reverse: list use cases on an entry.
	euc, err := s.ListEntryUseCases(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(euc) != 1 || euc[0].ID != uc.ID {
		t.Fatalf("ListEntryUseCases: %+v", euc)
	}

	// Unlink.
	if err := s.UnlinkUseCaseEntry(ctx, uc.ID, id1); err != nil {
		t.Fatal(err)
	}
	_, total2, _ := s.ListUseCaseEntries(ctx, uc.ID, 10, 0)
	if total2 != 1 {
		t.Fatalf("after unlink: want 1, got %d", total2)
	}
}

func TestListUseCasesSummaryAndPaging(t *testing.T) {
	s, id1 := seed(t)
	ctx := context.Background()
	id2, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "lesson", Title: "B", Body: "b"})
	if err := s.CreateProject(ctx, &Project{ID: "q", Name: "Q"}); err != nil {
		t.Fatal(err)
	}
	id3, err := s.CreateEntry(ctx, &Entry{ProjectID: "q", Type: "trap", Title: "C", Body: "c"})
	if err != nil {
		t.Fatal(err)
	}

	// 3 use cases, last created updated last.
	uc1, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "A", NameEN: "Alpha", Domain: "lipsync"})
	uc2, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "B", NameEN: "Beta", Domain: "audio"})
	uc3, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "G", NameEN: "Gamma", Domain: "lipsync"})

	_ = s.LinkUseCaseEntry(ctx, uc1.ID, id1, "test")
	_ = s.LinkUseCaseEntry(ctx, uc1.ID, id2, "test")
	_ = s.LinkUseCaseEntry(ctx, uc2.ID, id3, "test")
	// uc3 has no entries.

	out, total, err := s.ListUseCases(ctx, UseCaseFilter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(out) != 3 {
		t.Fatalf("total=%d len=%d", total, len(out))
	}

	// updated_at ordering: linking bumps uc1/uc2 to be newest. Last link
	// (uc2 with id3) makes uc2 newest, then uc1 (last link was id2),
	// then uc3 (no links, but created last so still tied with original ts).
	// All happens within the same test second, so we mostly check id3
	// gets EntryCount=0 and the linked counts match.
	byID := map[string]*UseCaseSummary{}
	for _, s := range out {
		byID[s.ID] = s
	}
	if byID[uc1.ID].EntryCount != 2 {
		t.Errorf("uc1 EntryCount: %d", byID[uc1.ID].EntryCount)
	}
	if byID[uc2.ID].EntryCount != 1 {
		t.Errorf("uc2 EntryCount: %d", byID[uc2.ID].EntryCount)
	}
	if byID[uc3.ID].EntryCount != 0 {
		t.Errorf("uc3 EntryCount: %d", byID[uc3.ID].EntryCount)
	}
	if len(byID[uc1.ID].SampleEntries) != 2 {
		t.Errorf("uc1 sample: %d", len(byID[uc1.ID].SampleEntries))
	}

	// Filter by project — uc2 has an entry in project "q" only.
	outQ, _, err := s.ListUseCases(ctx, UseCaseFilter{ProjectID: "q"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(outQ) != 1 || outQ[0].ID != uc2.ID {
		t.Fatalf("ProjectID filter: %+v", outQ)
	}

	// Filter by domain.
	outL, _, _ := s.ListUseCases(ctx, UseCaseFilter{Domain: "lipsync"}, 10, 0)
	if len(outL) != 2 {
		t.Fatalf("Domain filter: want 2, got %d", len(outL))
	}

	// Filter by query — match en_name.
	outQry, _, _ := s.ListUseCases(ctx, UseCaseFilter{Query: "amma"}, 10, 0)
	if len(outQry) != 1 || outQry[0].ID != uc3.ID {
		t.Fatalf("Query filter: %+v", outQry)
	}

	// Paging.
	page1, total1, _ := s.ListUseCases(ctx, UseCaseFilter{}, 2, 0)
	if total1 != 3 || len(page1) != 2 {
		t.Fatalf("page1: total=%d len=%d", total1, len(page1))
	}
	page2, _, _ := s.ListUseCases(ctx, UseCaseFilter{}, 2, 2)
	if len(page2) != 1 {
		t.Fatalf("page2: want 1, got %d", len(page2))
	}
}

func TestUseCaseTree(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()

	// 3 leaves
	a, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉A", NameEN: "Leaf A"})
	b, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉B", NameEN: "Leaf B"})
	c, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉C", NameEN: "Leaf C"})

	// All three start as top-level.
	top, total, err := s.ListUseCases(ctx, UseCaseFilter{Level: "top"}, 10, 0)
	if err != nil || total != 3 {
		t.Fatalf("level=top initial: total=%d err=%v", total, err)
	}
	for _, r := range top {
		if r.ParentID != "" {
			t.Errorf("expected empty parent_id for %s, got %q", r.ID, r.ParentID)
		}
		if r.ChildCount != 0 {
			t.Errorf("expected ChildCount=0 for %s, got %d", r.ID, r.ChildCount)
		}
	}

	// Stack a meta above A and B.
	meta, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "メタAB", NameEN: "Meta AB"})
	if err := s.SetUseCaseParent(ctx, a.ID, meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUseCaseParent(ctx, b.ID, meta.ID); err != nil {
		t.Fatal(err)
	}

	// Top-level now: meta + C (not a, not b).
	top, total, _ = s.ListUseCases(ctx, UseCaseFilter{Level: "top"}, 10, 0)
	if total != 2 {
		t.Fatalf("level=top after meta stack: want 2, got %d", total)
	}
	ids := map[string]int{}
	for _, r := range top {
		ids[r.ID] = r.ChildCount
	}
	if _, ok := ids[meta.ID]; !ok || ids[meta.ID] != 2 {
		t.Errorf("meta should be top-level with ChildCount=2, got %v", ids)
	}
	if _, ok := ids[c.ID]; !ok {
		t.Errorf("c should still be top-level, got %v", ids)
	}
	if _, ok := ids[a.ID]; ok {
		t.Errorf("a should NOT be top-level after parent stacking, got %v", ids)
	}

	// Drill down into meta.
	children, total, _ := s.ListUseCases(ctx, UseCaseFilter{ParentID: meta.ID}, 10, 0)
	if total != 2 || len(children) != 2 {
		t.Fatalf("children of meta: want 2, got %d", total)
	}
	childIDs := map[string]bool{children[0].ID: true, children[1].ID: true}
	if !childIDs[a.ID] || !childIDs[b.ID] {
		t.Errorf("children should be a,b: got %v", childIDs)
	}
	for _, ch := range children {
		if ch.ParentID != meta.ID {
			t.Errorf("child %s parent_id: want %s, got %s", ch.ID, meta.ID, ch.ParentID)
		}
	}

	// Un-rooting back to top.
	if err := s.SetUseCaseParent(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUseCase(ctx, a.ID)
	if got.ParentID != "" {
		t.Fatalf("after un-root: want empty parent, got %q", got.ParentID)
	}

	// A cannot be its own parent.
	if err := s.SetUseCaseParent(ctx, a.ID, a.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self-parent: want ErrInvalidInput, got %v", err)
	}
}

// TestUseCaseDescendantCount verifies a META reports rolled-up entry counts
// (its leaves' entries) rather than its own always-zero direct count, and
// that DeleteUseCase prunes an empty node while re-parenting its children.
func TestUseCaseDescendantCount(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	const projID = "p" // seed() creates project "p"

	// Two leaves under a meta, each with linked entries.
	leaf1, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉1", NameEN: "Leaf One"})
	leaf2, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉2", NameEN: "Leaf Two"})
	meta, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "メタ", NameEN: "Meta Group"})
	_ = s.SetUseCaseParent(ctx, leaf1.ID, meta.ID)
	_ = s.SetUseCaseParent(ctx, leaf2.ID, meta.ID)

	// 2 entries on leaf1, 1 on leaf2.
	for i, leaf := range []*UseCase{leaf1, leaf1, leaf2} {
		eid, err := s.CreateEntry(ctx, &Entry{
			ProjectID: projID, Type: "trap", Title: "e", Body: "b", Status: "ACTIVE",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.LinkUseCaseEntry(ctx, leaf.ID, eid, "test"); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}

	// Meta is top-level; its DescendantEntryCount must be 3 (rolled up),
	// while its direct EntryCount stays 0.
	top, _, _ := s.ListUseCases(ctx, UseCaseFilter{Level: "top"}, 10, 0)
	var m *UseCaseSummary
	for _, r := range top {
		if r.ID == meta.ID {
			m = r
		}
	}
	if m == nil {
		t.Fatal("meta not found at top level")
	}
	if m.EntryCount != 0 {
		t.Errorf("meta direct EntryCount: want 0, got %d", m.EntryCount)
	}
	if m.DescendantEntryCount != 3 {
		t.Errorf("meta DescendantEntryCount: want 3 (rolled up), got %d", m.DescendantEntryCount)
	}

	// Delete refuses while a leaf has linked entries.
	if err := s.DeleteUseCase(ctx, leaf1.ID); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("delete with linked entries: want ErrInvalidInput, got %v", err)
	}

	// An empty leaf deletes cleanly.
	empty, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "空", NameEN: "Empty Leaf"})
	if err := s.DeleteUseCase(ctx, empty.ID); err != nil {
		t.Fatalf("delete empty leaf: %v", err)
	}
	if _, err := s.GetUseCase(ctx, empty.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted leaf still present: %v", err)
	}
}

// TestEntryFilterUncategorized verifies the indexer work-feed: only entries
// with NO use_case link appear, so the backlog is drainable (the old feed
// surfaced newest-regardless-of-membership and never reached the tail).
func TestEntryFilterUncategorized(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	const projID = "p"

	// Three substantive entries; link one to a use_case.
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := s.CreateEntry(ctx, &Entry{
			ProjectID: projID, Type: "trap", Title: "t", Body: "b", Status: "ACTIVE",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	uc, _ := s.UpsertUseCase(ctx, &UseCase{NameJA: "葉", NameEN: "Leaf"})
	if err := s.LinkUseCaseEntry(ctx, uc.ID, ids[0], "test"); err != nil {
		t.Fatal(err)
	}

	// Uncategorized feed must return the 2 unlinked, never the linked one.
	got, total, err := s.ListEntries(ctx, EntryFilter{
		Type: "trap", Uncategorized: true, OldestFirst: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	// seed() also creates one trap, so traps = 4, one linked → 3 uncategorised.
	if total != 3 {
		t.Fatalf("uncategorized total: want 3, got %d", total)
	}
	for _, e := range got {
		if e.ID == ids[0] {
			t.Errorf("linked entry %s leaked into uncategorized feed", e.ID)
		}
	}
	// OldestFirst: the seed entry (created first, in seed()) or ids order —
	// just assert ascending by created order among our three: ids[1] before ids[2].
	var pos1, pos2 = -1, -1
	for i, e := range got {
		if e.ID == ids[1] {
			pos1 = i
		}
		if e.ID == ids[2] {
			pos2 = i
		}
	}
	if pos1 >= 0 && pos2 >= 0 && pos1 > pos2 {
		t.Errorf("oldest-first ordering broken: ids[1] at %d after ids[2] at %d", pos1, pos2)
	}

	// NotProgressedByRole: a record the indexer decided to skip (progress
	// recorded, never linked) must drop out of the uncategorized feed, or
	// it would be re-read every session forever.
	if err := s.RecordProgress(ctx, &LibrarianProgress{
		Role: "indexer", EntryID: ids[1], Action: "skipped_record",
	}); err != nil {
		t.Fatal(err)
	}
	got2, total2, err := s.ListEntries(ctx, EntryFilter{
		Type: "trap", Uncategorized: true, NotProgressedByRole: "indexer", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 2 {
		t.Fatalf("after skip-progress: want 2 (3 uncategorised − 1 skipped), got %d", total2)
	}
	for _, e := range got2 {
		if e.ID == ids[1] {
			t.Errorf("skipped record %s still in feed", e.ID)
		}
	}
}
