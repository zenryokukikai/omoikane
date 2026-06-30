package dashboard

import (
	"context"
	"html/template"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

func TestBrowsePages(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	root, err := s.CreateHierarchyNode(ctx, &store.HierarchyNode{
		Name: "root", Description: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := mount(t, s, true)

	code, body := get(t, srv, "/browse", "")
	if code != 200 || !strings.Contains(string(body), "root") {
		t.Fatalf("roots: %d body=%s", code, body[:200])
	}

	code, body = get(t, srv, "/browse/"+root, "")
	if code != 200 || !strings.Contains(string(body), "root") {
		t.Fatalf("node: %d body=%s", code, body[:200])
	}

	code, _ = get(t, srv, "/browse/missing", "")
	if code != 404 {
		t.Fatalf("missing: %d", code)
	}
}

func TestIndexPage(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	for _, gb := range []string{"", "tag", "recent", "hierarchy", "junk"} {
		path := "/index"
		if gb != "" {
			path = "/index?group_by=" + gb
		}
		code, _ := get(t, srv, path, "")
		if code != 200 {
			t.Fatalf("%s: code=%d", path, code)
		}
	}
}

func TestEntryPageBacklinksAndWikiLinks(t *testing.T) {
	s := newDashStore(t)
	id := seedEntries(t, s)
	ctx := context.Background()

	// Make a second entry that links back to id
	id2, _ := s.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Title: "linker",
		Body: "see [[" + id + "]] for details",
	})
	_ = s.CreateRelation(ctx, &store.Relation{
		FromID: id2, ToID: id, RelType: "related",
	})

	srv := mount(t, s, true)
	code, body := get(t, srv, "/entries/"+id, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(string(body), "Backlinks") {
		t.Fatalf("missing Backlinks section")
	}

	// The linker page should render the wikiLink as an anchor.
	code, body = get(t, srv, "/entries/"+id2, "")
	if code != 200 {
		t.Fatalf("linker status=%d", code)
	}
	if !strings.Contains(string(body), `class="wiki"`) {
		t.Fatalf("missing wiki link class: %s", string(body)[:500])
	}
}

func TestWikiLinksHelper(t *testing.T) {
	// Plain entry-id link
	out := string(wikiLinks("see [[T-ABCD]] now", ""))
	if !strings.Contains(out, `href="/entries/T-ABCD"`) {
		t.Fatalf("entries: %s", out)
	}
	// With token
	out = string(wikiLinks("[[T-X]]", "abc"))
	if !strings.Contains(out, "token=abc") {
		t.Fatalf("token: %s", out)
	}
	// Custom label
	out = string(wikiLinks("[[T-X|alt label]]", ""))
	if !strings.Contains(out, ">alt label<") {
		t.Fatalf("label: %s", out)
	}
	// H- → /browse
	out = string(wikiLinks("[[H-1]]", ""))
	if !strings.Contains(out, "/browse/H-1") {
		t.Fatalf("hier: %s", out)
	}
	// SIT- → /situations
	out = string(wikiLinks("[[SIT-1]]", ""))
	if !strings.Contains(out, "/situations/SIT-1") {
		t.Fatalf("sit: %s", out)
	}
	// CL- → /clusters
	out = string(wikiLinks("[[CL-1]]", ""))
	if !strings.Contains(out, "/clusters/CL-1") {
		t.Fatalf("cl: %s", out)
	}
	// Non-link content is HTML-escaped
	out = string(wikiLinks("<script>alert(1)</script>", ""))
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("xss: %s", out)
	}
	// No-link text passes through
	out = string(wikiLinks("plain text", ""))
	if out != "plain text" {
		t.Fatalf("plain: %s", out)
	}
	// Unknown prefix falls back to /entries (default branch)
	out = string(wikiLinks("[[CASE-1]]", ""))
	if !strings.Contains(out, "/entries/CASE-1") && !strings.Contains(out, "/case") {
		// CASE- isn't routed specially; default branch produces /entries/CASE-1
		t.Fatalf("case fallback: %s", out)
	}
	// Sanity: returns template.HTML
	var _ template.HTML = wikiLinks("x", "")
}

// Phase 4 page error paths (internal error when underlying tables go away).
func TestPhase4PagesInternalError(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	// Disable FK checks so we can drop dependent tables in any order.
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE hierarchy_nodes",
		"DROP TABLE entries",
		"DROP TABLE tags",
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("drop %q: %v", q, err)
		}
	}
	for _, p := range []string{"/browse", "/index", "/browse/missing"} {
		code, _ := get(t, srv, p, "")
		if code == 200 {
			t.Fatalf("%s: expected non-200, got %d", p, code)
		}
	}
}
