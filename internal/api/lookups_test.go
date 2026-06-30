package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// seedEntryWithIndices creates a kb project + entry and populates the
// reverse indices manually so lookup tests don't rely on the enrichment
// pipeline.
func seedEntryWithIndices(t *testing.T, st *store.Store, tags []string,
	symptoms []string, triggers []store.IndexedTrigger, prohibited string) string {
	t.Helper()
	ctx := context.Background()
	_ = st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"})
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "kb", Type: "trap", Title: "Mask trap",
		Body: "rect mask leaks", Prohibited: prohibited,
		Tags:    tags,
		Status:  "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(symptoms) > 0 {
		if err := st.ReplaceSymptoms(ctx, id, symptoms, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(triggers) > 0 {
		if err := st.ReplaceTriggers(ctx, id, triggers, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestLookupByTriggerEndpoint(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, []string{"mask"}, nil,
		[]store.IndexedTrigger{{Phrase: "modify mask generation", Domain: "preprocessing"}},
		"DO NOT reintroduce rectangular masking")

	s, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{
			"trigger_description": "I want to modify mask generation logic",
			"domain":              "preprocessing",
			"include_prohibited":  true,
		}, nil)
	if s != 200 {
		t.Fatalf("status=%d body=%s", s, raw)
	}
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 1 || out.Matches[0]["entry_id"] != id {
		t.Fatalf("unexpected matches: %+v", out.Matches)
	}
	if !contains(out.Matches[0]["prohibited"].(string), "rectangular") {
		t.Fatalf("expected prohibited surfaced: %v", out.Matches[0])
	}
}

func TestLookupByTriggerProhibitedPresenceFlag(t *testing.T) {
	// Without include_prohibited, the response should hide the actual
	// text but still indicate presence.
	base, tok, st := testServer(t)
	seedEntryWithIndices(t, st, []string{"mask"}, nil,
		[]store.IndexedTrigger{{Phrase: "modify mask generation", Domain: ""}},
		"some forbidden pattern")

	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "modify mask generation"}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) == 0 {
		t.Fatal("expected a match")
	}
	if !contains(out.Matches[0]["prohibited"].(string), "(present") {
		t.Fatalf("expected presence flag: %v", out.Matches[0])
	}
}

func TestLookupByTriggerProjectFilter(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, []string{"mask"}, nil,
		[]store.IndexedTrigger{{Phrase: "modify mask generation", Domain: ""}}, "")
	// Same trigger, different project — should NOT appear when filter set.
	_ = st.CreateProject(context.Background(), &store.Project{ID: "other", Name: "Other"})

	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{
			"trigger_description": "modify mask generation",
			"project_id":          "other",
		}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 0 {
		t.Fatalf("project filter ineffective: %+v (entry %s should be excluded)", out.Matches, id)
	}
}

func TestLookupByTriggerHidesSuperseded(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, nil, nil,
		[]store.IndexedTrigger{{Phrase: "modify mask generation"}}, "")
	// Soft-delete (status=ARCHIVED) → lookup should hide it.
	if err := st.SoftDeleteEntry(context.Background(), id, "", ""); err != nil {
		t.Fatal(err)
	}
	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "modify mask generation"}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 0 {
		t.Fatalf("archived entries should not appear: %+v", out.Matches)
	}
}

func TestLookupByTriggerSkipsDeletedEntries(t *testing.T) {
	// Cover the buildLookupResponse branch where GetEntry returns
	// ErrNotFound (orphaned reverse-index row). FK enforcement blocks
	// inserting a trigger pointing at a non-existent entry, so we disable
	// it for this single INSERT.
	base, tok, st := testServer(t)
	if _, err := st.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO triggers_index(entry_id, phrase, phrase_normalized, domain, source)
		 VALUES (?, ?, ?, ?, ?)`,
		"T-ORPHAN", "modify mask generation", "modify mask generation", "", "test"); err != nil {
		t.Fatal(err)
	}
	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "modify mask generation"}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 0 {
		t.Fatalf("orphan hit should be filtered, got %+v", out.Matches)
	}
}

func TestLookupByTriggerEmpty(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "   "}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestLookupByTriggerBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/lookup/by-trigger",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestLookupByTriggerStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop trigger_rules so the rule-load query fails (this fires before
	// the FTS layer and surfaces an error regardless of the query length).
	dropTable(t, st, "trigger_rules")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-trigger", tok,
		map[string]any{"trigger_description": "modify mask generation"}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestLookupBySymptomEndpoint(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, nil,
		[]string{"rectangular artifact at inference"}, nil, "")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{"symptom_description": "rectangular artifact"}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 1 || out.Matches[0]["entry_id"] != id {
		t.Fatalf("unexpected: %+v", out.Matches)
	}
}

func TestLookupBySymptomBadInputs(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{"symptom_description": ""}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/lookup/by-symptom",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Fatalf("bad json status=%d", resp.StatusCode)
	}
}

func TestLookupBySymptomStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "symptoms_index")
	// Use a longer query so ftsTokenise actually produces FTS terms.
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-symptom", tok,
		map[string]any{"symptom_description": "rectangular artifact at inference"}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestLookupByTagsEndpoint(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedEntryWithIndices(t, st, []string{"mask", "preprocessing"}, nil, nil, "")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/lookup/by-tags", tok,
		map[string]any{"tags": []string{"mask"}, "match_mode": "any"}, nil)
	var out struct {
		Matches []map[string]any `json:"matches"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Matches) != 1 || out.Matches[0]["entry_id"] != id {
		t.Fatalf("unexpected: %+v", out.Matches)
	}
}

func TestLookupByTagsBadInputs(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-tags", tok,
		map[string]any{"tags": []string{}}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/lookup/by-tags",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 400 {
		t.Fatalf("bad json status=%d", resp.StatusCode)
	}
}

func TestLookupByTagsStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "tags")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/lookup/by-tags", tok,
		map[string]any{"tags": []string{"x"}}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// helpers

func contains(haystack, needle string) bool {
	return needle != "" && haystack != "" && bytes.Contains([]byte(haystack), []byte(needle))
}
