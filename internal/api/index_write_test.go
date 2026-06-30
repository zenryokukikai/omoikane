package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// TestPutEntryIndexRevivesLookup is the end-to-end proof that the indexer's
// write endpoint makes the reverse-lookup subsystem return hits: without an
// LLM nothing populates symptoms_index/triggers_index, so we POST the phrases
// via /v1/entries/{id}/index and then confirm /v1/lookup/by-symptom and
// /v1/lookup/by-trigger find the entry.
func TestPutEntryIndexRevivesLookup(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "kb", Type: "trap", Title: "Audio noise on resume",
		Body: "noise appears after resuming training", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Index is empty: by-symptom returns nothing yet.
	s, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{"symptom_description": "音声 ノイズ", "include_prohibited": true}, nil)
	if s != 200 {
		t.Fatalf("pre-lookup status=%d body=%s", s, raw)
	}
	var pre struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &pre)
	if len(pre.Matches) != 0 {
		t.Fatalf("expected empty index before write, got %d", len(pre.Matches))
	}

	// Indexer writes symptoms + triggers.
	s, raw = doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/index", tok,
		map[string]any{
			"symptoms": []string{"音声 ノイズ", "resume後のノイズ"},
			"triggers": []map[string]any{
				{"phrase": "training resume noise", "domain": "audio"},
			},
			"source": "indexer-test",
		}, nil)
	if s != 200 {
		t.Fatalf("index write status=%d body=%s", s, raw)
	}
	var wrote putIndexResponse
	if err := json.Unmarshal(raw, &wrote); err != nil {
		t.Fatal(err)
	}
	if wrote.Symptoms != 2 || wrote.Triggers != 1 || wrote.EntryID != id {
		t.Fatalf("unexpected write response: %+v", wrote)
	}

	// Now by-symptom finds it.
	s, raw = doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{"symptom_description": "音声 ノイズ", "include_prohibited": true}, nil)
	if s != 200 {
		t.Fatalf("post-lookup status=%d body=%s", s, raw)
	}
	var post struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &post)
	if len(post.Matches) == 0 || post.Matches[0]["entry_id"] != id {
		t.Fatalf("by-symptom did not find the entry after indexing: %+v", post.Matches)
	}

	// And by-trigger finds it.
	s, raw = doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "training resume noise", "domain": "audio",
			"include_prohibited": true}, nil)
	if s != 200 {
		t.Fatalf("by-trigger status=%d body=%s", s, raw)
	}
	var trig struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &trig)
	if len(trig.Matches) == 0 || trig.Matches[0]["entry_id"] != id {
		t.Fatalf("by-trigger did not find the entry after indexing: %+v", trig.Matches)
	}
}

// TestPutEntryIndexRejectsEmpty confirms a request with no phrases is a 400.
func TestPutEntryIndexRejectsEmpty(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"})
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "kb", Type: "lesson", Title: "x", Body: "y", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/index", tok,
		map[string]any{"symptoms": []string{}, "triggers": []map[string]any{}}, nil)
	if s != 400 {
		t.Fatalf("expected 400 for empty index write, got %d body=%s", s, raw)
	}
}

// TestPutEntryIndexUnknownEntry confirms a missing entry is a 404.
func TestPutEntryIndexUnknownEntry(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries/T-NOPE/index", tok,
		map[string]any{"symptoms": []string{"x"}}, nil)
	if s != 404 {
		t.Fatalf("expected 404 for unknown entry, got %d body=%s", s, raw)
	}
}
