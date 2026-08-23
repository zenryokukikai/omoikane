package dashboard

// Router completeness guard for the dashboard page-leak matrix (issue
// #99 item 3) — the twin of internal/api/space_leak_guard_test.go.
//
// TestDashboardLeakMatrixCompleteness walks every (method, pattern) the
// dashboard's chi router registers and asserts each one is EITHER a row
// in dashLeakRows (GET pages) OR listed in the ledger below with a
// reason (the machine form of the "DELIBERATELY OUTSIDE THE MATRIX"
// header list in space_leak_page_test.go). A new page added without a
// row or a ledger entry fails this test. Stale ledger entries and rows
// that no longer match a route fail too.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type dashLedgerEntry struct {
	method  string
	pattern string
	reason  string
}

// dashNotCovered — reasons for the page-class entries are spelled out
// in the space_leak_page_test.go header; the strings here name the
// class and the pinning test.
var dashNotCovered = []dashLedgerEntry{
	// ---- public pages (header: static / capability-code metadata) ----
	{"GET", "/login", "public: login landing, no entry content"},
	{"GET", "/skill.md", "public: static skill document"},
	{"GET", "/samples/agent-helpers/{name}", "public: static sample scripts"},
	{"GET", "/claim/{code}", "public: invitation metadata behind an unguessable capability code"},
	{"GET", "/members/claim/{code}", "public: invitation metadata behind an unguessable capability code"},
	{"GET", "/static/style.css", "static asset, no data"},

	// ---- header's DELIBERATELY-OUTSIDE page classes ----
	{"GET", "/directives", "operator watch-topics, no entry linkage (header)"},
	{"GET", "/talk", "renders only the signed-in user's OWN threads; the thread page IS a row (header)"},
	{"GET", "/agents", "viewer's own agents + invitations: user metadata (header)"},
	{"GET", "/u/{id}", "public profile: all-visible user metadata (header)"},
	{"GET", "/members", "human directory: admin-scope-gated, members_test.go pins the gate (header)"},
	{"GET", "/admin/spaces", "space/group names are all-visible metadata; admin-gated, admin_spaces_test.go (header)"},

	// ---- write surfaces of the same classes ----
	{"POST", "/agents/issue", "viewer's own agent invitation issuance: user metadata (agents_test.go)"},
	{"POST", "/u/{id}/edit", "profile self-edit, ownership pinned in profile_test.go"},
	{"POST", "/members/invite", "admin-gated member invitation (members_test.go)"},
	{"POST", "/members/{id}/role", "admin-gated role change (members_test.go)"},
	{"POST", "/admin/spaces/create", "admin-gated space/group management (admin_spaces_test.go)"},
	{"POST", "/admin/groups/create", "admin-gated space/group management (admin_spaces_test.go)"},
	{"POST", "/admin/groups/{id}/members/add", "admin-gated space/group management (admin_spaces_test.go)"},
	{"POST", "/admin/groups/{id}/members/remove", "admin-gated space/group management (admin_spaces_test.go)"},
	{"POST", "/admin/spaces/{id}/acl", "admin-gated space/group management (admin_spaces_test.go)"},
	{"POST", "/admin/spaces/{id}/acl/remove", "admin-gated space/group management (admin_spaces_test.go)"},

	// ---- covered by a dedicated leak test outside the row table ----
	{"POST", "/chat/{id}/post", "covered elsewhere: talk-thread write gate pinned by TestDashboardChatWriteRefusesTalkThreads"},
	{"POST", "/chat/{id}/close", "covered elsewhere: talk-thread write gate pinned by TestDashboardChatWriteRefusesTalkThreads"},

	// ---- flagged at guard introduction ----
	{"GET", "/my/librarian", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (viewer's own librarian settings page, #73)"},
	{"POST", "/my/librarian", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (viewer's own librarian settings save, #73)"},
	{"GET", "/librarian-icon/{userID}", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (librarian avatar image, #73)"},
	{"POST", "/chat/new", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (opens a caller-owned coordination thread)"},
}

// normalizeDashPattern strips a query string and collapses every
// "{param}" segment to "{}" (twin of the api package's
// normalizeLeakPattern; test helpers cannot be shared across packages).
func normalizeDashPattern(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimSuffix(p, "/")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

type dashRouteKey struct{ method, pattern string }

func TestDashboardLeakMatrixCompleteness(t *testing.T) {
	h, err := New(newDashStore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	h.Mount(r)

	registered := map[dashRouteKey]string{}
	walkErr := chi.Walk(r,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			registered[dashRouteKey{method, normalizeDashPattern(route)}] = route
			return nil
		})
	if walkErr != nil {
		t.Fatalf("chi.Walk: %v", walkErr)
	}
	if len(registered) == 0 {
		t.Fatal("chi.Walk found no routes — guard is not walking the router")
	}

	covered := map[dashRouteKey]bool{}
	for _, row := range dashLeakRows() {
		covered[dashRouteKey{"GET", normalizeDashPattern(row.path)}] = true
	}
	ledger := map[dashRouteKey]bool{}
	for _, e := range dashNotCovered {
		k := dashRouteKey{e.method, normalizeDashPattern(e.pattern)}
		if ledger[k] {
			t.Errorf("duplicate ledger entry: %s %s", e.method, e.pattern)
		}
		ledger[k] = true
	}

	// (1) Every registered route has a matrix row or a ledger entry.
	var missing []string
	for k, orig := range registered {
		if !covered[k] && !ledger[k] {
			missing = append(missing, k.method+" "+orig)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("dashboard route %s has neither a dashLeakRows row nor a dashNotCovered entry — add one with a reason", m)
	}

	// (2) Ledger hygiene: live route, and not shadowing a covered row.
	for _, e := range dashNotCovered {
		k := dashRouteKey{e.method, normalizeDashPattern(e.pattern)}
		if _, ok := registered[k]; !ok {
			t.Errorf("stale ledger entry %s %s: no such registered route", e.method, e.pattern)
		}
		if covered[k] {
			t.Errorf("ledger entry %s %s is also covered by a matrix row — remove it from the ledger", e.method, e.pattern)
		}
	}

	// (3) Row hygiene: every row must address a live GET route.
	for _, row := range dashLeakRows() {
		k := dashRouteKey{"GET", normalizeDashPattern(row.path)}
		if _, ok := registered[k]; !ok {
			t.Errorf("matrix row %q (GET %s) matches no registered route", row.name, row.path)
		}
	}
}
