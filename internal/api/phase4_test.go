package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

func TestBrowseAndIndex(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)

	// Create root node
	s, raw := doJSON(t, http.MethodPost, base+"/v1/browse", tok,
		map[string]any{"name": "root", "project_id": "p"}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}
	var n struct {
		ID string `json:"ID"`
	}
	_ = json.Unmarshal(raw, &n)
	if n.ID == "" {
		t.Fatalf("no ID: %s", raw)
	}

	// Attach entries
	s, _ = doJSON(t, http.MethodPost, base+"/v1/browse/"+n.ID+"/entries", tok,
		map[string]any{"entry_id": a, "weight": 0.9}, nil)
	if s != 204 {
		t.Fatalf("attach a: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/browse/"+n.ID+"/entries", tok,
		map[string]any{"entry_id": b}, nil)
	if s != 204 {
		t.Fatalf("attach b: %d", s)
	}

	// Browse roots
	s, _ = doJSON(t, http.MethodGet, base+"/v1/browse", tok, nil, nil)
	if s != 200 {
		t.Fatalf("roots: %d", s)
	}

	// Browse node detail
	s, raw = doJSON(t, http.MethodGet, base+"/v1/browse/"+n.ID, tok, nil, nil)
	if s != 200 {
		t.Fatalf("node: %d %s", s, raw)
	}

	// Browse node entries
	s, raw = doJSON(t, http.MethodGet, base+"/v1/browse/"+n.ID+"/entries", tok, nil, nil)
	if s != 200 {
		t.Fatalf("node-entries: %d %s", s, raw)
	}

	// Detach
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/browse/"+n.ID+"/entries/"+b, tok, nil, nil)
	if s != 204 {
		t.Fatalf("detach: %d", s)
	}

	// Index by tag / recent / hierarchy
	for _, g := range []string{"tag", "recent", "hierarchy"} {
		s, _ = doJSON(t, http.MethodGet, base+"/v1/index?group_by="+g+"&project_id=p", tok, nil, nil)
		if s != 200 {
			t.Fatalf("index %s: %d", g, s)
		}
	}
	// Bad group_by
	s, _ = doJSON(t, http.MethodGet, base+"/v1/index?group_by=junk", tok, nil, nil)
	if s != 400 {
		t.Fatalf("bad-index: %d", s)
	}

	// Delete node
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/browse/"+n.ID, tok, nil, nil)
	if s != 204 {
		t.Fatalf("delete: %d", s)
	}
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/browse/missing", tok, nil, nil)
	if s != 404 {
		t.Fatalf("delete-missing: %d", s)
	}
}

func TestBrowseValidation(t *testing.T) {
	base, tok, _ := testServer(t)
	// Missing name
	s, _ := doJSON(t, http.MethodPost, base+"/v1/browse", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-name: %d", s)
	}
	// Bad JSON
	if got := postRaw(t, http.MethodPost, base+"/v1/browse", tok, "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}
	if got := postRaw(t, http.MethodPost, base+"/v1/browse/xxx/entries", tok, "{"); got != 400 {
		t.Fatalf("bad-attach-json: %d", got)
	}
	// Attach without entry_id
	s, _ = doJSON(t, http.MethodPost, base+"/v1/browse/xxx/entries", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-entry_id: %d", s)
	}
	// Get missing node
	s, _ = doJSON(t, http.MethodGet, base+"/v1/browse/missing", tok, nil, nil)
	if s != 404 {
		t.Fatalf("missing-node: %d", s)
	}
}

// ============================================================
// /v1/reflect
// ============================================================

func TestReflect(t *testing.T) {
	base, tok, st := testServer(t)
	a, b := phase3SeedEntries(t, st)
	_ = st

	s, raw := doJSON(t, http.MethodPost, base+"/v1/reflect", tok,
		map[string]any{"entry_ids": []string{a, b, "missing"}, "prompt": "compare"}, nil)
	if s != 200 {
		t.Fatalf("status=%d %s", s, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["engine"] != "heuristic" {
		t.Fatalf("engine: %v", out["engine"])
	}
	if summary, _ := out["summary"].(string); summary == "" {
		t.Fatalf("empty summary: %v", out)
	}
}

func TestReflectValidation(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/reflect", tok,
		map[string]any{"entry_ids": []string{}}, nil)
	if s != 400 {
		t.Fatalf("empty: %d", s)
	}
	if got := postRaw(t, http.MethodPost, base+"/v1/reflect", tok, "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}
}

// ============================================================
// /v1/search?mode=reasoning
// ============================================================

func TestSearchModeReasoning(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st,
		[]string{"foo"}, []string{"observed symptom abc"}, nil, "")
	// Add a misleading case to penalise this entry's helpfulness.
	caseID, _ := st.CreateCase(context.Background(), &store.UsageCase{EntryID: id})
	result := "misleading"
	_, _ = st.PatchCase(context.Background(), caseID, store.CasePatch{Result: &result})

	s, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": `"abc"*`, "mode": "reasoning"}, nil)
	if s != 200 {
		t.Fatalf("search: %d %s", s, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["mode"] != "reasoning" {
		t.Fatalf("mode echo: %v", out["mode"])
	}
}

func TestSearchModeDefault(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "x"}, nil)
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["mode"] != "fts" {
		t.Fatalf("default mode: %v", out["mode"])
	}
}

