package api

// Router completeness guard for the space-leak matrix (issue #99 item 3).
//
// TestSpaceLeakMatrixCompleteness walks every (method, pattern) the chi
// router registers and asserts each one is EITHER
//
//   (a) present in the union of the per-domain leak-case tables
//       (space_leak_entries_test.go / _aggregates_ / _threads_), or
//   (b) listed in the explicit not-covered ledger below with a reason.
//
// A new route added without a leak row or a ledger entry FAILS this
// test — the "every entry-carrying route gets a row" convention is now
// machine-checked instead of eyeballed. The guard also fails on stale
// state: a ledger entry (or matrix row) that no longer matches any
// registered route, and a ledger entry for a route that IS covered.
//
// Matching: patterns are compared segment-by-segment after collapsing
// every "{param}" segment to "{}" and stripping the row paths' query
// strings — so the tables' fixture placeholders ("{secret}", "{task}")
// match chi's ("{id}") without a clever regex.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/config"
	"github.com/zenryokukikai/omoikane/internal/enrich"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// leakLedgerEntry is one route deliberately outside the leak matrix.
type leakLedgerEntry struct {
	method  string
	pattern string // chi pattern form
	reason  string
}

// leakNotCovered is the explicit not-covered ledger (formerly the
// prose "NOT YET COVERED" list in space_leak_test.go's header). Every
// entry names its class; the classes are:
//
//   public          unauthenticated endpoints with no entry content
//   metadata        all-visible metadata by design (v2 residual risk)
//   admin ops       admin-scope-gated operational surface
//   covered elsewhere  a dedicated leak test outside the row tables
var leakNotCovered = []leakLedgerEntry{
	// ---- public: no auth, no entry content ----
	{"GET", "/v1/health", "public: static status payload"},
	{"GET", "/v1/auth/google/login", "public: OAuth initiation, no entry content"},
	{"GET", "/v1/auth/google/callback", "public: OAuth callback, no entry content"},
	{"POST", "/v1/auth/logout", "public: clears the caller's session cookie"},
	{"POST", "/v1/agents/register", "public: agent self-onboarding, user metadata only (agents_test.go)"},
	{"GET", "/v1/agents/claim/{code}", "public: invitation metadata behind an unguessable capability code"},
	{"POST", "/v1/agents/claim/{code}", "invitation redemption: user metadata only (agents_test.go)"},
	{"GET", "/v1/auth/me", "caller's own identity, no entry content"},

	// ---- all-visible metadata by design (v2 residual risk) ----
	{"GET", "/v1/users", "metadata: public profile directory (neutral naming is the operating rule)"},
	{"GET", "/v1/users/{id}", "metadata: public profile"},
	{"PATCH", "/v1/users/me", "metadata: caller's own profile self-edit"},
	{"GET", "/v1/projects", "metadata: project names are all-visible by design"},
	{"GET", "/v1/projects/{id}", "metadata: project names are all-visible by design"},
	{"POST", "/v1/projects", "metadata: creates org-wide project metadata, no space linkage"},
	{"PATCH", "/v1/projects/{id}", "metadata: edits org-wide project metadata"},
	{"POST", "/v1/librarian/instances", "metadata: librarian seat registry (librarian scope)"},
	{"GET", "/v1/librarian/instances", "metadata: librarian seat registry"},
	{"GET", "/v1/librarian/instances/{id}", "metadata: librarian seat registry"},
	{"PATCH", "/v1/librarian/instances/{id}", "metadata: librarian seat status"},
	{"POST", "/v1/librarian/instances/{id}/heartbeat", "metadata: librarian seat heartbeat"},
	{"GET", "/v1/librarian/directives", "metadata: operator watch-topics, no entry linkage"},
	{"POST", "/v1/librarian/directives", "metadata: operator watch-topics"},
	{"PATCH", "/v1/librarian/directives/{id}", "metadata: operator watch-topics"},
	{"DELETE", "/v1/librarian/directives/{id}", "metadata: operator watch-topics"},
	{"GET", "/v1/librarian/quartet", "metadata: coordination artefact (topic + role names), no entry linkage in payload"},
	{"POST", "/v1/librarian/quartet", "metadata: coordination artefact"},
	{"POST", "/v1/librarian/quartet/{id}/decide", "metadata: coordination artefact"},
	{"POST", "/v1/librarian/coordinator/propose_quartet", "metadata: proposes a quartet (coordination artefact)"},
	{"POST", "/v1/librarian/findings", "metadata: records neutral external source excerpt; the entry-touching edge (correlate) IS a matrix row"},

	// ---- admin-scope-gated ops (RequireScope pins the gate) ----
	{"POST", "/v1/admin/agent-invites", "caller's own agent invitations: user metadata (agents_test.go)"},
	{"GET", "/v1/admin/agent-invites", "caller's own agent invitations: user metadata"},
	{"POST", "/v1/admin/members/invitations", "admin ops: member invitations (members_test.go)"},
	{"GET", "/v1/admin/members/invitations", "admin ops: member invitations"},
	{"PATCH", "/v1/admin/users/{id}/role", "admin ops: role change, user metadata"},
	{"POST", "/v1/admin/webhooks", "admin ops: webhook config; space_scope DELIVERY is asserted in space_leak_slice4_test.go"},
	{"GET", "/v1/admin/webhooks", "admin ops: webhook config"},
	{"PATCH", "/v1/admin/webhooks/{id}", "admin ops: webhook config"},
	{"DELETE", "/v1/admin/webhooks/{id}", "admin ops: webhook config"},
	{"POST", "/v1/admin/backup", "admin ops: backup trigger"},
	{"GET", "/v1/admin/backups", "admin ops: backup listing"},
	{"POST", "/v1/admin/dead_pool/run", "admin ops: dead-pool sweep"},
	{"GET", "/v1/admin/health/llm_usage", "admin ops: usage counters"},
	{"GET", "/v1/admin/health/coverage", "admin ops: coverage counters"},
	{"GET", "/v1/admin/spaces", "admin ops: space/group management, org-wide metadata (admin_spaces_test.go)"},
	{"POST", "/v1/admin/spaces", "admin ops: space/group management"},
	{"PUT", "/v1/admin/spaces/{id}/acl/{groupID}", "admin ops: space/group management"},
	{"DELETE", "/v1/admin/spaces/{id}/acl/{groupID}", "admin ops: space/group management"},
	{"GET", "/v1/admin/groups", "admin ops: space/group management"},
	{"POST", "/v1/admin/groups", "admin ops: space/group management"},
	{"PUT", "/v1/admin/groups/{id}/members/{userID}", "admin ops: space/group management"},
	{"DELETE", "/v1/admin/groups/{id}/members/{userID}", "admin ops: space/group management"},
	{"POST", "/v1/clusters/rebuild", "admin ops: admin-only; clusters the internal space exclusively (store-enforced)"},
	{"POST", "/v1/librarian/emergency_stop", "admin ops: kill switch, no entry content"},

	// ---- covered by dedicated leak tests outside the row tables ----
	{"GET", "/v1/events", "covered elsewhere: SSE space_scope asserted event-by-event in space_leak_slice4_test.go"},
	{"POST", "/v1/events/broadcast", "covered elsewhere: broadcast thread-gating asserted in space_leak_slice4_test.go"},
	{"POST", "/v1/attachments", "covered elsewhere: multipart upload contract pinned by TestAttachmentSpaceUpload"},

	// ---- flagged at guard introduction ----
	{"POST", "/v1/librarian/threads", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (opens a caller-owned thread; related_entries visibility unaudited)"},
	{"POST", "/v1/librarian/tasks", "TODO(#99): 未分類 — ガード導入時点の既存未カバー (enqueue ignores space_id; space is stamped only via open-work claim)"},
}

// normalizeLeakPattern strips a query string and collapses every
// "{param}" path segment to "{}", so "/v1/entries/{secret}?as_of=x"
// and chi's "/v1/entries/{id}" compare equal.
func normalizeLeakPattern(p string) string {
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

// leakGuardRouter mounts the full API surface (same construction as
// testServer, minus the HTTP listener) so chi.Walk can enumerate it.
func leakGuardRouter(t *testing.T) chi.Router {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store:       st,
		Enricher:    enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsEnforce,
		Logger:      logger,
	}
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

type leakRouteKey struct{ method, pattern string }

func TestSpaceLeakMatrixCompleteness(t *testing.T) {
	registered := map[leakRouteKey]string{} // normalized → original pattern
	err := chi.Walk(leakGuardRouter(t),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			registered[leakRouteKey{method, normalizeLeakPattern(route)}] = route
			return nil
		})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if len(registered) == 0 {
		t.Fatal("chi.Walk found no routes — guard is not walking the router")
	}

	covered := map[leakRouteKey]bool{}
	for _, row := range leakMatrixRows() {
		covered[leakRouteKey{row.method, normalizeLeakPattern(row.path)}] = true
	}
	ledger := map[leakRouteKey]bool{}
	for _, e := range leakNotCovered {
		k := leakRouteKey{e.method, normalizeLeakPattern(e.pattern)}
		if ledger[k] {
			t.Errorf("duplicate ledger entry: %s %s", e.method, e.pattern)
		}
		ledger[k] = true
	}

	// (1) The invariant: every registered route has a matrix row or a
	// ledger entry.
	var missing []string
	for k, orig := range registered {
		if !covered[k] && !ledger[k] {
			missing = append(missing, k.method+" "+orig)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("route %s has neither a leak-matrix row nor a leakNotCovered entry — add a row to its domain table (space_leak_*_test.go) or a ledger entry with a reason", m)
	}

	// (2) Ledger hygiene: entries must match a live route, and must not
	// shadow a route the matrix already covers.
	for _, e := range leakNotCovered {
		k := leakRouteKey{e.method, normalizeLeakPattern(e.pattern)}
		if _, ok := registered[k]; !ok {
			t.Errorf("stale ledger entry %s %s: no such registered route", e.method, e.pattern)
		}
		if covered[k] {
			t.Errorf("ledger entry %s %s is also covered by a matrix row — remove it from the ledger", e.method, e.pattern)
		}
	}

	// (3) Row hygiene: every matrix row must address a live route
	// (catches typos and routes removed after the fact).
	for _, row := range leakMatrixRows() {
		k := leakRouteKey{row.method, normalizeLeakPattern(row.path)}
		if _, ok := registered[k]; !ok {
			t.Errorf("matrix row %q (%s %s) matches no registered route", row.name, row.method, row.path)
		}
	}
}
