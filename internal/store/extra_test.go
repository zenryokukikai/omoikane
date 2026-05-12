package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestListProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "a", Name: "A"})
	_ = s.CreateProject(ctx, &Project{ID: "b", Name: "B"})
	ps, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("count=%d", len(ps))
	}
}

func TestGetUser(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u", Name: "U"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "member" {
		t.Fatalf("default role=%q", got.Role)
	}
	if _, err := s.GetUser(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateUserAndTokenInvalid(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateUser(context.Background(), &User{ID: ""}); err != ErrInvalidInput {
		t.Fatalf("got %v", err)
	}
	if _, err := s.CreateToken(context.Background(), "", "", []string{"read"}, nil); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := s.CreateToken(context.Background(), "", "n", nil, nil); err == nil {
		t.Fatal("expected error for empty scopes")
	}
}

func TestReplaceTagsClears(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
		Tags: []string{"a", "b"},
	})
	if err := s.ReplaceTags(ctx, id, nil, "llm"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEntry(ctx, id)
	if len(got.Tags) != 0 {
		t.Fatalf("expected cleared, got %v", got.Tags)
	}
}

func TestNullTimeBoxParsesFormats(t *testing.T) {
	var n nullTimeBox
	if err := (&n).Scan(nil); err != nil || n.Valid {
		t.Fatalf("nil: %v %v", err, n.Valid)
	}
	if err := (&n).Scan(time.Now().UTC()); err != nil || !n.Valid {
		t.Fatalf("time.Time: %v %v", err, n.Valid)
	}
	if err := (&n).Scan([]byte("2026-05-12 10:00:00")); err != nil || !n.Valid {
		t.Fatalf("sqlite ts: %v", err)
	}
	if err := (&n).Scan("2026-05-12T10:00:00Z"); err != nil || !n.Valid {
		t.Fatalf("rfc3339: %v", err)
	}
	if err := (&n).Scan("garbage"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchFiltersByProjectAndType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "a", Name: "A"})
	_ = s.CreateProject(ctx, &Project{ID: "b", Name: "B"})
	_, _ = s.CreateEntry(ctx, &Entry{ProjectID: "a", Type: "trap", Title: "mask", Body: "mask leaks"})
	_, _ = s.CreateEntry(ctx, &Entry{ProjectID: "b", Type: "trap", Title: "mask other", Body: "mask too"})
	res, _, err := s.SearchFTS(ctx, `"mask"*`, EntryFilter{ProjectID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Entry.ProjectID != "a" {
		t.Fatalf("filter failed: %+v", res)
	}
}

func TestEmptyFTSQuery(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.SearchFTS(context.Background(), "  ", EntryFilter{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSourceFromRole(t *testing.T) {
	cases := map[string]string{
		"":              "human",
		"human":         "human",
		"token:cli":     "human",
		"librarian:cataloger": "librarian",
		"agent":         "agent",
	}
	for in, want := range cases {
		if got := sourceFromRole(in); got != want {
			t.Errorf("%q: want %s got %s", in, want, got)
		}
	}
}

func TestEntryHistoryUnknown(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.EntryHistory(context.Background(), "T-NOPE"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListEntriesByTag(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "a", Body: "a",
		Tags: []string{"mask"},
	})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "b", Body: "b",
	})
	es, total, err := s.ListEntries(ctx, EntryFilter{Tag: "mask"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(es) != 1 {
		t.Fatalf("tag filter ineffective: total=%d", total)
	}
}

func TestUpdateEntryBadStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})
	bad := "BOGUS"
	_, _, err := s.UpdateEntry(ctx, id, EntryPatch{Status: &bad, ExpectedVersion: 1})
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("expected invalid-status error, got %v", err)
	}
}

func TestUpdateEntryUnknownID(t *testing.T) {
	s := newTestStore(t)
	title := "x"
	_, _, err := s.UpdateEntry(context.Background(), "T-NOPE",
		EntryPatch{Title: &title, ExpectedVersion: 1})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
