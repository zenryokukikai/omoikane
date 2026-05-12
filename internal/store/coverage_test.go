package store

import (
	"context"
	"strings"
	"testing"
)

func TestValidEntryTypeAll(t *testing.T) {
	all := []EntryType{
		TypeTrap, TypeDecision, TypeDesign, TypeLesson, TypeIncident,
		TypeLibrarianMeta, TypeExternalFinding,
	}
	for _, ty := range all {
		if !ValidEntryType(string(ty)) {
			t.Errorf("type %s should be valid", ty)
		}
	}
	if ValidEntryType("nope") {
		t.Error("nope should be invalid")
	}
}

func TestValidStatusAll(t *testing.T) {
	all := []Status{
		StatusDraft, StatusInvestigating, StatusActive, StatusSuperseded,
		StatusArchived, StatusDuplicate, StatusResolved,
	}
	for _, s := range all {
		if !ValidStatus(string(s)) {
			t.Errorf("status %s should be valid", s)
		}
	}
	if ValidStatus("nope") {
		t.Error("nope should be invalid")
	}
}

func TestCreateEntryRequiresProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "missing", Type: "trap", Title: "x", Body: "y",
	}); err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestCreateEntryRequiresType(t *testing.T) {
	s := newTestStore(t)
	_ = s.CreateProject(context.Background(), &Project{ID: "p", Name: "P"})
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "garbage", Title: "x", Body: "y",
	})
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("expected invalid-type error, got %v", err)
	}
}

func TestCreateEntryNilReturnsInvalid(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateEntry(context.Background(), nil); err != ErrInvalidInput {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestEncodeDecodeTagsSnapshot(t *testing.T) {
	in := []string{"a", "b", "c"}
	out := decodeTagsSnapshot(encodeTagsSnapshot(in))
	if len(out) != 3 || out[0] != "a" {
		t.Fatalf("roundtrip failed: %v", out)
	}
	if decodeTagsSnapshot("") != nil {
		t.Fatal("empty should be nil")
	}
}

func TestUpdateEntryEmptyTitleFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})
	empty := ""
	_, _, err := s.UpdateEntry(ctx, id, EntryPatch{Title: &empty, ExpectedVersion: 1})
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("expected empty-title error, got %v", err)
	}
}

func TestUpdateEntryEmptyBodyFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})
	empty := ""
	_, _, err := s.UpdateEntry(ctx, id, EntryPatch{Body: &empty, ExpectedVersion: 1})
	if err == nil || !strings.Contains(err.Error(), "body") {
		t.Fatalf("expected empty-body error, got %v", err)
	}
}

func TestUpdateEntryWithoutOCC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})

	// ExpectedVersion = 0 → OCC skipped (admin path)
	title := "new"
	v, _, err := s.UpdateEntry(ctx, id, EntryPatch{Title: &title})
	if err != nil {
		t.Fatalf("update without OCC: %v", err)
	}
	if v != 2 {
		t.Fatalf("version=%d", v)
	}
}

func TestListPaginationLimits(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	for i := 0; i < 5; i++ {
		s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "trap", Title: "x", Body: "y",
		})
	}
	first, total, err := s.ListEntries(ctx, EntryFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || total != 5 {
		t.Fatalf("page 1: got %d/%d", len(first), total)
	}
	second, _, _ := s.ListEntries(ctx, EntryFilter{Limit: 2, Offset: 2})
	if first[0].ID == second[0].ID {
		t.Fatal("offset did not advance")
	}
}

func TestDBHandle(t *testing.T) {
	s := newTestStore(t)
	if s.DB() == nil {
		t.Fatal("DB() should expose underlying *sql.DB")
	}
}
