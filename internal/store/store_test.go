package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsApplied(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		versions = append(versions, v)
	}
	if len(versions) < 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("expected 1,2 applied, got %v", versions)
	}
}

func TestProjectCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "dup"}); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
	got, err := s.GetProject(ctx, "p")
	if err != nil || got.Name != "P" {
		t.Fatalf("get: %v, name=%q", err, got.Name)
	}
	if _, err := s.GetProject(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateGetEntry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})

	id, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "Mask",
		Body: "rect mask leaks", Tags: []string{"Mask", "mask", "preprocessing"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(id, "T-") {
		t.Fatalf("expected T- prefix, got %s", id)
	}
	got, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Fatalf("version=%d", got.Version)
	}
	if got.Status != "DRAFT" {
		t.Fatalf("default status: %q", got.Status)
	}
	if got.ValidTo != nil {
		t.Fatalf("valid_to should be NULL for new entry")
	}
	// dedup case-folded tags
	if len(got.Tags) != 2 {
		t.Fatalf("tags=%v", got.Tags)
	}
}

func TestUpdateOCC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})

	// Mismatched version
	title := "new"
	if _, _, err := s.UpdateEntry(ctx, id, EntryPatch{Title: &title, ExpectedVersion: 99}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
	// Matching version succeeds
	newVer, updated, err := s.UpdateEntry(ctx, id, EntryPatch{Title: &title, ExpectedVersion: 1})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if newVer != 2 || updated.Title != "new" {
		t.Fatalf("update result: v=%d title=%q", newVer, updated.Title)
	}
	// History has 2 rows
	hist, err := s.EntryHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len=%d", len(hist))
	}
}

func TestAsOfReconstructsHistoricalState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})

	id, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "v1 title", Body: "v1 body",
	})
	createdAt := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)

	title := "v2 title"
	body := "v2 body"
	if _, _, err := s.UpdateEntry(ctx, id, EntryPatch{
		Title: &title, Body: &body, ExpectedVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// As-of just after creation should show v1.
	asOf := createdAt.Add(10 * time.Millisecond)
	snap, err := s.GetEntryAsOf(ctx, id, asOf)
	if err != nil {
		t.Fatalf("as_of: %v", err)
	}
	if snap.Title != "v1 title" || snap.Body != "v1 body" || snap.Version != 1 {
		t.Fatalf("as_of v1 wrong: %+v", snap)
	}
	// As-of in far past = NotFound
	if _, err := s.GetEntryAsOf(ctx, id, createdAt.Add(-time.Hour)); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for pre-creation as_of, got %v", err)
	}
	// As-of now = current (v2)
	cur, err := s.GetEntryAsOf(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if cur.Title != "v2 title" {
		t.Fatalf("as_of now should be v2, got %q", cur.Title)
	}
}

func TestSoftDeleteSetsValidity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})

	if err := s.SoftDeleteEntry(ctx, id, "tester", "human"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEntry(ctx, id)
	if got.Status != "ARCHIVED" {
		t.Fatalf("status=%q", got.Status)
	}
	if got.ValidTo == nil {
		t.Fatal("valid_to should be set after archive")
	}
	if got.Version != 2 {
		t.Fatalf("version=%d", got.Version)
	}
	// idempotent
	if err := s.SoftDeleteEntry(ctx, id, "", ""); err != nil {
		t.Fatalf("second delete should be idempotent, got %v", err)
	}
}

func TestListEntriesExcludesSupersededByDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	live, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "live", Body: "."})
	gone, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "gone", Body: "."})
	_ = s.SoftDeleteEntry(ctx, gone, "", "")

	es, total, err := s.ListEntries(ctx, EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(es) != 1 || es[0].ID != live {
		t.Fatalf("default list should hide ARCHIVED: total=%d, got=%+v", total, es)
	}
	es, total, err = s.ListEntries(ctx, EntryFilter{IncludeSuperseded: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(es) != 2 {
		t.Fatalf("include_superseded should return both: %d/%d", len(es), total)
	}
}

func TestSearchFTS(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "Mask trap",
		Body: "Use landmark-driven soft mask, not rectangular mask.",
	})
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "decision", Title: "Adopt SyncNet",
		Body: "Adopting SyncNet for lipsync evaluation.",
	})
	res, total, err := s.SearchFTS(ctx, `"mask"*`, EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(res) != 1 || res[0].Entry.Title != "Mask trap" {
		t.Fatalf("search returned wrong result: %+v", res)
	}
}

func TestTokenLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	plain, err := s.CreateToken(ctx, "admin", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.LookupToken(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !HasScope(tok.Scopes, "write") {
		t.Fatal("expected write")
	}
	if _, err := s.LookupToken(ctx, "garbage"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "u", Name: "u"})
	past := time.Now().Add(-time.Hour)
	plain, _ := s.CreateToken(ctx, "u", "x", []string{"read"}, &past)
	if _, err := s.LookupToken(ctx, plain); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for expired token, got %v", err)
	}
}

func TestAuditWrite(t *testing.T) {
	s := newTestStore(t)
	err := s.WriteAudit(context.Background(), AuditEvent{
		Method: "POST", Path: "/v1/entries", StatusCode: 201, DurationMs: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 audit row, got %d", n)
	}
}

func TestSetEnrichment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "x", Body: "y"})
	if err := s.SetEnrichment(ctx, id, 5); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEntry(ctx, id)
	if got.EnrichmentVersion != 5 || got.EnrichmentAt == nil {
		t.Fatalf("enrichment not recorded: %+v", got)
	}
}

func TestTypePrefixes(t *testing.T) {
	cases := map[EntryType]string{
		TypeTrap: "T", TypeDecision: "D", TypeDesign: "X",
		TypeLesson: "L", TypeIncident: "I",
		TypeLibrarianMeta: "M", TypeExternalFinding: "F",
	}
	for typ, want := range cases {
		if got := typePrefix(string(typ)); got != want {
			t.Errorf("%s: want %s got %s", typ, want, got)
		}
	}
}

func TestNormaliseTagsCapsAt20(t *testing.T) {
	var in []string
	for i := 0; i < 30; i++ {
		in = append(in, "t"+string(rune('A'+i)))
	}
	out := normaliseTags(in)
	if len(out) != 20 {
		t.Fatalf("expected cap 20, got %d", len(out))
	}
}
