package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// seedEntriesList creates a project and a mix of entry types so the
// /entries filter UI has something meaningful to discriminate on.
func seedEntriesList(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct{ Type, Title string }{
		{"trap", "trap-a"},
		{"trap", "trap-b"},
		{"lesson", "lesson-a"},
		{"librarian_meta", "summary-of-trap-a"},
	} {
		if _, err := s.CreateEntry(ctx, &store.Entry{
			ProjectID: "p", Type: seed.Type, Title: seed.Title,
			Body: "x", Status: "ACTIVE",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// /entries with no filter renders all (within limit) and shows the
// filter form.
func TestEntriesListNoFilter(t *testing.T) {
	s := newDashStore(t)
	seedEntriesList(t, s)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/entries")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	// All four titles should be present
	for _, want := range []string{"trap-a", "trap-b", "lesson-a", "summary-of-trap-a"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected to find %q in body", want)
		}
	}
	// Form should be rendered
	if !strings.Contains(body, `<form method="get" action="/entries"`) {
		t.Error("filter form missing")
	}
	// "Showing N of M" counter
	if !strings.Contains(body, "matching entries") {
		t.Error("counter text missing")
	}
}

// /entries?type=librarian_meta restricts to that type only.
// This is the exact URL the dashboard nav should serve.
func TestEntriesListTypeFilter(t *testing.T) {
	s := newDashStore(t)
	seedEntriesList(t, s)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/entries?type=librarian_meta")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	if !strings.Contains(body, "summary-of-trap-a") {
		t.Error("librarian_meta entry should appear")
	}
	for _, hidden := range []string{"trap-a", "trap-b", "lesson-a"} {
		// title appears as link text; we just make sure it's not in the body
		if strings.Contains(body, ">"+hidden+"<") {
			t.Errorf("non-librarian_meta title %q leaked through type=librarian_meta filter", hidden)
		}
	}
	// The selected option should reflect the active filter
	if !strings.Contains(body, `value="librarian_meta"  selected`) {
		t.Error("filter select should preserve active value")
	}
}

// /entries?project=<id> filters by project.
func TestEntriesListProjectFilter(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &store.Project{ID: "a", Name: "A"})
	_ = s.CreateProject(ctx, &store.Project{ID: "b", Name: "B"})
	_, _ = s.CreateEntry(ctx, &store.Entry{
		ProjectID: "a", Type: "trap", Title: "in-a", Body: "x", Status: "ACTIVE",
	})
	_, _ = s.CreateEntry(ctx, &store.Entry{
		ProjectID: "b", Type: "trap", Title: "in-b", Body: "x", Status: "ACTIVE",
	})
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/entries?project=a")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, "in-a") {
		t.Error("project=a entry should appear")
	}
	if strings.Contains(body, ">in-b<") {
		t.Error("project=b entry should NOT appear under project=a filter")
	}
}

// Home page nav should link to /entries so it's discoverable.
func TestHomeNavIncludesEntries(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `href="/entries`) {
		t.Error("home page should link to /entries from the subnav")
	}
}

// Home page should also surface the common type filters as quick-view
// links so users don't have to know URL params. This is the discovery
// path for "where do I see librarian summaries".
func TestHomeQuickViewLinks(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, link := range []string{
		`href="/entries?type=external_finding`,
		`href="/entries?type=trap`,
		`href="/entries?type=lesson`,
		`href="/entries?status=DRAFT`,
	} {
		if !strings.Contains(body, link) {
			t.Errorf("home page should include quick-view link %q", link)
		}
	}
}

// /entries page should also surface quick filter chips at the top so
// even users who landed without a token-bearing URL can navigate.
func TestEntriesPageQuickViewChips(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/entries")
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, "Quick views:") {
		t.Error("/entries should label the chip row 'Quick views:'")
	}
	for _, link := range []string{
		`href="/entries?type=librarian_meta`,
		`href="/entries?type=trap`,
		`href="/entries?type=lesson`,
	} {
		if !strings.Contains(body, link) {
			t.Errorf("/entries should include quick-view chip %q", link)
		}
	}
}

// The active quick view is identifiable (issue #120): a filter that IS a
// quick view marks exactly that link active; a filter that matches no view
// (a project refinement) highlights nothing.
func TestEntriesQuickViewActiveMarker(t *testing.T) {
	s := newDashStore(t)
	seedEntriesList(t, s)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// ?type=trap → the traps view is the active one, "all" is not.
	resp, _ := http.Get(srv.URL + "/entries?type=trap")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	if !strings.Contains(body, `class="active" aria-current="page">⚠️ traps</a>`) {
		t.Error("type=trap should mark the traps quick view active")
	}
	if strings.Contains(body, `class="active" aria-current="page">all</a>`) {
		t.Error("the 'all' quick view must not be active under type=trap")
	}

	// ?project=p matches no quick view → nothing is highlighted.
	resp, _ = http.Get(srv.URL + "/entries?project=p")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), `aria-current="page"`) {
		t.Error("a project-only filter matches no quick view; none should be active")
	}
}

// The empty state names the filters actually in effect and offers a way to
// clear them (issue #120). No incident is seeded, so ?type=incident is
// empty.
func TestEntriesEmptyStateNamesFilters(t *testing.T) {
	s := newDashStore(t)
	seedEntriesList(t, s)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/entries?type=incident")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)

	for _, want := range []string{
		"この条件に一致するエントリはありません",
		"種別",
		"incident",
		// The clear action keeps space+token (both empty here) and drops the
		// type — so it points at bare /entries, with the pager-link chrome.
		`class="pager-link" href="/entries"`,
		"絞り込みを解除",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty state should contain %q", want)
		}
	}
	// The old reason-less, dead-end message is gone.
	if strings.Contains(body, "No entries match the current filter.") {
		t.Error("the old empty-state message should be removed")
	}
}
