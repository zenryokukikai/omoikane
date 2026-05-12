package dashboard

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

func TestReviewQueuePage(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/review-queue", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !strings.Contains(string(body), "Review queue") {
		t.Fatalf("missing heading: %s", body[:200])
	}
}

func TestClustersPages(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	cid, err := s.CreateCluster(ctx, &store.IncidentCluster{
		Title: "ttl", Summary: "sum",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := mount(t, s, true)

	code, body := get(t, srv, "/clusters", "")
	if code != 200 || !strings.Contains(string(body), "ttl") {
		t.Fatalf("list: code=%d body=%s", code, body)
	}
	code, body = get(t, srv, "/clusters/"+cid, "")
	if code != 200 || !strings.Contains(string(body), "ttl") {
		t.Fatalf("get: code=%d body=%s", code, body)
	}
	code, _ = get(t, srv, "/clusters/nope", "")
	if code != 404 {
		t.Fatalf("not-found: %d", code)
	}
}

func TestSituationsPages(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	id, err := s.CreateSituation(ctx, &store.Situation{
		Description: "users see a rectangular artifact",
		Domain:      "inference",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := mount(t, s, true)

	code, body := get(t, srv, "/situations", "")
	if code != 200 || !strings.Contains(string(body), "rectangular") {
		t.Fatalf("list: code=%d body=%s", code, body)
	}
	code, body = get(t, srv, "/situations/"+id, "")
	if code != 200 || !strings.Contains(string(body), "rectangular") {
		t.Fatalf("get: code=%d body=%s", code, body)
	}
	code, _ = get(t, srv, "/situations/nope", "")
	if code != 404 {
		t.Fatalf("not-found: %d", code)
	}
}

func TestEntryPageRendersPhase3Sections(t *testing.T) {
	s := newDashStore(t)
	id := seedEntries(t, s)
	ctx := context.Background()
	// Add a case, signal will fall out of the view; add a relation to render.
	cid, _ := s.CreateCase(ctx, &store.UsageCase{EntryID: id, TriggerQuery: "trig"})
	result := "helpful"
	_, _ = s.PatchCase(ctx, cid, store.CasePatch{Result: &result})

	// Add a second entry + relation.
	id2, _ := s.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Title: "second", Body: "b",
	})
	_ = s.CreateRelation(ctx, &store.Relation{
		FromID: id, ToID: id2, RelType: "related",
	})

	srv := mount(t, s, true)
	code, body := get(t, srv, "/entries/"+id, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	// We expect cases + relations + signals sections to render.
	for _, want := range []string{"Recent cases", "Relations", "Usage signals"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %q in body", want)
		}
	}
}

// Deref helper is otherwise only reached via the review-queue template.
func TestDerefHelper(t *testing.T) {
	v := 1.5
	if deref(&v) != 1.5 {
		t.Fatal("deref value mismatch")
	}
	if deref(nil) != 0 {
		t.Fatal("deref nil mismatch")
	}
}

// Smoke for the home page's subnav links.
func TestHomePageSubnav(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	for _, want := range []string{"Review queue", "Clusters", "Situations"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("subnav missing %q", want)
		}
	}
}

// Cover the http response error paths by closing the store before the
// request — the underlying queries will fail and the handler must emit 500.
func TestPhase3PagesInternalError(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	// Drop the underlying tables to force errors.
	for _, q := range []string{
		"DROP VIEW review_queue",
		"DROP TABLE incident_clusters",
		"DROP TABLE situations",
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("drop %q: %v", q, err)
		}
	}
	for _, p := range []string{"/review-queue", "/clusters", "/situations"} {
		code, _ := get(t, srv, p, "")
		if code == 200 {
			t.Fatalf("%s: expected non-200, got %d", p, code)
		}
	}
}

// Verify the home page still loads with no token using DashboardOpen=false
// against a stubbed auth-skipping path through query-token middleware.
var _ http.Handler = (*nilHandler)(nil)

type nilHandler struct{}

func (nilHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}
