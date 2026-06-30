package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// phase3SeedEntries creates two entries in project "p" and returns their IDs.
func phase3SeedEntries(t *testing.T, st *store.Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	_ = st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"})
	a, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Title: "A", Body: "a", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Title: "B", Body: "b", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// ============================================================
// cases
// ============================================================

func TestCaseAPILifecycle(t *testing.T) {
	base, tok, st := testServer(t)
	a, _ := phase3SeedEntries(t, st)

	s, raw := doJSON(t, http.MethodPost, base+"/v1/cases", tok,
		map[string]any{"entry_id": a, "trigger_query": "want to do thing"}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}
	var c struct {
		CaseID string `json:"CaseID"`
	}
	_ = json.Unmarshal(raw, &c)
	if c.CaseID == "" {
		t.Fatalf("no case_id: %s", raw)
	}

	// PATCH outcome+result
	s, raw = doJSON(t, http.MethodPatch, base+"/v1/cases/"+c.CaseID, tok,
		map[string]any{"outcome": "applied", "result": "helpful"}, nil)
	if s != 200 {
		t.Fatalf("patch: %d %s", s, raw)
	}

	// GET case
	s, _ = doJSON(t, http.MethodGet, base+"/v1/cases/"+c.CaseID, tok, nil, nil)
	if s != 200 {
		t.Fatalf("get: %d", s)
	}

	// list cases for entry
	s, raw = doJSON(t, http.MethodGet, base+"/v1/entries/"+a+"/cases", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}
	var lr struct {
		Cases []map[string]any `json:"cases"`
	}
	_ = json.Unmarshal(raw, &lr)
	if len(lr.Cases) != 1 {
		t.Fatalf("len=%d", len(lr.Cases))
	}

	// signals
	s, _ = doJSON(t, http.MethodGet, base+"/v1/entries/"+a+"/signals", tok, nil, nil)
	if s != 200 {
		t.Fatalf("signals: %d", s)
	}
}

func TestCaseAPIMissingEntryID(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/cases", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("expected 400, got %d", s)
	}
}

func TestCaseAPIBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	for _, c := range []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/cases"},
		{http.MethodPatch, "/v1/cases/xxx"},
	} {
		s := postRaw(t, c.method, base+c.path, tok, "not-json")
		if s != 400 {
			t.Fatalf("%s %s: status=%d", c.method, c.path, s)
		}
	}
}

// postRaw sends a request with arbitrary string body (used for bad-JSON tests).
func postRaw(t *testing.T, method, url, tok, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestReviewQueueEndpoint(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/review-queue?limit=10", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// ============================================================
// relations
// ============================================================

func TestRelationAPI(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)

	// Create
	s, raw := doJSON(t, http.MethodPost, base+"/v1/relations", tok,
		map[string]any{
			"from_id": a, "to_id": b, "rel_type": "related",
		}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}

	// List
	s, raw = doJSON(t, http.MethodGet, base+"/v1/entries/"+a+"/relations", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}

	// List incoming
	s, _ = doJSON(t, http.MethodGet, base+"/v1/entries/"+b+"/relations?direction=incoming", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list-incoming: %d", s)
	}

	// List both
	s, _ = doJSON(t, http.MethodGet, base+"/v1/entries/"+a+"/relations?direction=both", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list-both: %d", s)
	}

	// Delete
	s, _ = doJSON(t, http.MethodDelete,
		base+"/v1/relations?from_id="+a+"&to_id="+b+"&rel_type=related", tok, nil, nil)
	if s != 204 {
		t.Fatalf("delete: %d", s)
	}

	// Delete missing → 404
	s, _ = doJSON(t, http.MethodDelete,
		base+"/v1/relations?from_id="+a+"&to_id="+b+"&rel_type=related", tok, nil, nil)
	if s != 404 {
		t.Fatalf("delete-missing: %d", s)
	}
}

func TestRelationAPIValidation(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)

	// Missing fields
	s, _ := doJSON(t, http.MethodPost, base+"/v1/relations", tok,
		map[string]any{"from_id": a}, nil)
	if s != 400 {
		t.Fatalf("missing: %d", s)
	}
	// Bad rel_type
	s, _ = doJSON(t, http.MethodPost, base+"/v1/relations", tok,
		map[string]any{"from_id": a, "to_id": b, "rel_type": "junk"}, nil)
	if s != 400 {
		t.Fatalf("bad-type: %d", s)
	}
	// Bad JSON
	if got := postRaw(t, http.MethodPost, base+"/v1/relations", tok, "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}

	// Delete missing params → 400
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/relations", tok, nil, nil)
	if s != 400 {
		t.Fatalf("missing-params delete: %d", s)
	}
}

// ============================================================
// situations
// ============================================================

func TestSituationAPI(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)

	// Create with embedded entries
	s, raw := doJSON(t, http.MethodPost, base+"/v1/situations", tok,
		map[string]any{
			"description": "users see broken thing",
			"project_id":  "p",
			"entries": []map[string]any{
				{"entry_id": a, "relevance": 0.9},
			},
		}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}
	var sit struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(raw, &sit)

	// List
	s, _ = doJSON(t, http.MethodGet, base+"/v1/situations?project_id=p", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}

	// Get
	s, _ = doJSON(t, http.MethodGet, base+"/v1/situations/"+sit.ID, tok, nil, nil)
	if s != 200 {
		t.Fatalf("get: %d", s)
	}

	// Add entry
	s, _ = doJSON(t, http.MethodPost, base+"/v1/situations/"+sit.ID+"/entries", tok,
		map[string]any{"entry_id": b, "relevance": 0.5}, nil)
	if s != 204 {
		t.Fatalf("add-entry: %d", s)
	}

	// Remove entry
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/situations/"+sit.ID+"/entries/"+b, tok, nil, nil)
	if s != 204 {
		t.Fatalf("remove: %d", s)
	}

	// Lookup by situation
	s, _ = doJSON(t, http.MethodPost, base+"/v1/lookup/by-situation", tok,
		map[string]any{"situation_description": "users see broken"}, nil)
	if s != 200 {
		t.Fatalf("lookup: %d", s)
	}

	// Delete
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/situations/"+sit.ID, tok, nil, nil)
	if s != 204 {
		t.Fatalf("delete: %d", s)
	}
}

func TestSituationAPIValidation(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/situations", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-desc: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/lookup/by-situation", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-query: %d", s)
	}

	// Bad JSON paths
	for _, p := range []string{"/v1/situations", "/v1/lookup/by-situation"} {
		if got := postRaw(t, http.MethodPost, base+p, tok, "{"); got != 400 {
			t.Fatalf("bad-json %s: %d", p, got)
		}
	}

	// add-entry missing entry_id
	_, _ = doJSON(t, http.MethodPost, base+"/v1/situations", tok,
		map[string]any{"description": "x"}, nil)
}

// ============================================================
// clusters
// ============================================================

func TestClusterAPI(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)

	// Create
	s, raw := doJSON(t, http.MethodPost, base+"/v1/clusters", tok,
		map[string]any{"title": "T", "summary": "s", "project_id": "p"}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}
	var c struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(raw, &c)

	// Add member
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/"+c.ID+"/members", tok,
		map[string]any{"entry_id": a, "similarity": 0.8}, nil)
	if s != 204 {
		t.Fatalf("add: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/"+c.ID+"/members", tok,
		map[string]any{"entry_id": b, "similarity": 0.7}, nil)
	if s != 204 {
		t.Fatalf("add2: %d", s)
	}

	// Get + list
	s, _ = doJSON(t, http.MethodGet, base+"/v1/clusters/"+c.ID, tok, nil, nil)
	if s != 200 {
		t.Fatalf("get: %d", s)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/clusters?status=OPEN", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}

	// Remove member
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/clusters/"+c.ID+"/members/"+b, tok, nil, nil)
	if s != 204 {
		t.Fatalf("remove: %d", s)
	}

	// Promote
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/"+c.ID+"/promote", tok,
		map[string]any{"entry_id": a}, nil)
	if s != 204 {
		t.Fatalf("promote: %d", s)
	}

	// Dismiss (a fresh cluster)
	_, raw = doJSON(t, http.MethodPost, base+"/v1/clusters", tok,
		map[string]any{"title": "dum"}, nil)
	var c2 struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(raw, &c2)
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/"+c2.ID+"/dismiss", tok, nil, nil)
	if s != 204 {
		t.Fatalf("dismiss: %d", s)
	}

	// Rebuild (admin scope; our test token has admin)
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/rebuild", tok,
		map[string]any{"threshold": 0.3}, nil)
	if s != 200 {
		t.Fatalf("rebuild: %d", s)
	}
	// Rebuild with empty body
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/clusters/rebuild", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("rebuild-empty: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestClusterAPIValidation(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/clusters", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-title: %d", s)
	}
	// Bad JSON
	for _, p := range []string{
		"/v1/clusters",
		"/v1/clusters/xx/members",
		"/v1/clusters/xx/promote",
		"/v1/clusters/rebuild",
	} {
		if got := postRaw(t, http.MethodPost, base+p, tok, "{"); got != 400 {
			t.Fatalf("bad-json %s: %d", p, got)
		}
	}
	// Missing fields
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/xx/members", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-entry_id: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/clusters/xx/promote", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-promote-entry: %d", s)
	}
}

// ============================================================
// lookups with helpfulness + create_cases
// ============================================================

func TestLookupCreateCasesAttachesCaseID(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, []string{"foo"},
		[]string{"observed symptom xyz"}, nil, "")

	s, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{
			"symptom_description": "observed symptom xyz",
			"create_cases":        true,
		}, nil)
	if s != 200 {
		t.Fatalf("lookup: %d %s", s, raw)
	}
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) == 0 {
		t.Fatalf("expected at least one match: %s", raw)
	}
	if out.Matches[0]["entry_id"] != id {
		t.Fatalf("wrong entry: %v", out.Matches[0])
	}
	if _, ok := out.Matches[0]["case_id"].(string); !ok {
		t.Fatalf("expected case_id attached: %v", out.Matches[0])
	}
}

