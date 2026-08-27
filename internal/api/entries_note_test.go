package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// 'note' (issue #71) end to end through the ONE write path:
//  1. POST /v1/entries type=note → 201, N- prefixed id
//  2. it appears in GET /v1/entries?type=note and in POST /v1/search
//  3. it never enters the cataloger's backlog (a human memo needs no
//     summary) — neither backlog/next nor backlog_size see it.
func TestNoteEntryFlow(t *testing.T) {
	base, tok, st := testServer(t)
	if err := st.CreateProject(t.Context(), &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}

	// 1. create
	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "p", "type": "note",
		"title": "meeting memo", "body": "remember the zebra migration steps",
		"tags": []string{"memo"},
	}, nil)
	if s != http.StatusCreated {
		t.Fatalf("create note: %d %s", s, raw)
	}
	var created struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &created)
	if created.Type != "note" {
		t.Errorf("type = %q, want note", created.Type)
	}
	if !strings.HasPrefix(created.ID, "N-") {
		t.Errorf("note id should carry the N- prefix, got %q", created.ID)
	}

	// 2a. list filter
	s, raw = doJSON(t, http.MethodGet, base+"/v1/entries?type=note", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list notes: %d %s", s, raw)
	}
	var list struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(raw, &list)
	found := false
	for _, e := range list.Entries {
		if e.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("note %s missing from ?type=note list: %s", created.ID, raw)
	}

	// 2b. full-text search
	s, raw = doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "zebra"}, nil)
	if s != 200 {
		t.Fatalf("search: %d %s", s, raw)
	}
	if !strings.Contains(string(raw), created.ID) {
		t.Errorf("note %s missing from search results: %s", created.ID, raw)
	}

	// 3. cataloger backlog: empty — the note must not surface.
	s, raw = doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next?role=cataloger", tok, nil, nil)
	if s != http.StatusNotFound {
		t.Fatalf("cataloger backlog should be empty (note excluded), got %d %s", s, raw)
	}
}

// The create-entry error message advertises the full vocabulary,
// including 'note'.
func TestCreateEntryTypeErrorListsNote(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "p", "type": "memo", "title": "t", "body": "b",
	}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("bad type: %d", s)
	}
	if !strings.Contains(string(raw), "note") {
		t.Errorf("type error should list 'note': %s", raw)
	}
}
