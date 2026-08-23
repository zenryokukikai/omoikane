package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Fault aspect: backend store failures surfaced through the HTTP handlers —
// a closed store or a dropped table makes the handler's store call fail and
// the endpoint answer 500. The harness (newAPIStore, dropTable) lives in
// fault_test.go (and testServer/doJSON in api_test.go).

func TestCreateEntryStoreFailure(t *testing.T) {
	// Server's store is closed after seeding — POST /v1/entries should 500
	// because CreateEntry fails inside the handler.
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_ = st.Close()
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryStoreFailure(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	_ = st.Close()
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- deleteEntry edge cases ----

func TestDeleteEntryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	_ = st.Close()
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- getEntry edge cases ----

func TestGetEntryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/X", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestGetEntryAsOfStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet,
		base+"/v1/entries/X?as_of=2026-01-01T00:00:00Z", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- listEntries edge cases ----

func TestListEntriesStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- entryHistory edge cases ----

func TestEntryHistoryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/X/history", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- search edge cases ----

func TestSearchStoreErrorViaInvalidFTS(t *testing.T) {
	// SearchFTS returns ErrInvalidInput when given an empty trimmed
	// query — we already cover that. For the "default" branch in search()
	// (non-ErrInvalidInput store error), use a closed store.
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": `"x"*`}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- projects edge cases ----

func TestCreateProjectStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "projects")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "x", "name": "y"}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestListProjectsStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "projects")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/projects", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryGetEntryFails(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryUpdateStoreError(t *testing.T) {
	// Force UpdateEntry to return a non-version-mismatch error after the
	// initial GetEntry has succeeded. Drop the tags table — UpdateEntry's
	// PATCH writes back tag snapshots and fails there.
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	dropTable(t, st, "entry_history")
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("expected 500, got %d", s)
	}
}

func TestDeleteEntryNonNotFoundStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	// Drop entry_history → SoftDeleteEntry's history snapshot write fails
	// with a non-NotFound error.
	dropTable(t, st, "entry_history")
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 500 {
		t.Fatalf("expected 500, got %d", s)
	}
}
