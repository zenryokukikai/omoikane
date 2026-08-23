package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// Fault aspect: malformed or invalid client input — bad JSON, bad field
// types, bogus enum values, missing fields, secret-bearing bodies — plus
// the adjacent accepted-input companions of the same handlers. The harness
// lives in fault_test.go (and testServer/doJSON in api_test.go).

// ---- createEntry edge cases ----

func TestCreateEntryBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/entries",
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

func TestCreateEntryBadStatus(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
		"status": "BOGUS",
	}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestCreateEntryProjectMissing(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "does-not-exist",
		"type":       "trap",
		"title":      "x",
		"body":       "y",
	}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

// ---- updateEntry edge cases ----

func TestUpdateEntryBadIfMatch(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)

	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "not-a-number"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	req, _ := http.NewRequest(http.MethodPatch, base+"/v1/entries/"+id,
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestUpdateEntryBadField(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	// title must be a string; send a number
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"title": 42},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadTags(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"tags": "not-an-array"},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadStatus(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "BOGUS"},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithChangeSummary(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE", "change_summary": "promote"},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// ---- listEntries edge cases ----

func TestListEntriesBadLimitOffsetIgnored(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	// Bad limit/offset should silently fall back to defaults.
	s, _ := doJSON(t, http.MethodGet,
		base+"/v1/entries?limit=NaN&offset=BAD", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// ---- search edge cases ----

func TestSearchBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/search",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestSearchInvalidQueryReturns400(t *testing.T) {
	base, tok, _ := testServer(t)
	// Empty query string after the trim → 400.
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "   "}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

// ---- projects edge cases ----

func TestCreateProjectBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/projects",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestDeleteEntryNotFound(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/T-MISSING", tok, nil, nil)
	if s != 404 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithTags(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"tags": []string{"alpha", "beta"}},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntrySecretsRejected(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"body": "leaks ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012 here"},
		map[string]string{"If-Match": "1"})
	if s != 422 {
		t.Fatalf("expected 422, got %d", s)
	}
}

func TestCreateProjectMissingFields(t *testing.T) {
	base, tok, _ := testServer(t)
	// Missing name
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "x"}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithScopeAndMetadata(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{
			"scope":    map[string]any{"foo": "bar"},
			"metadata": map[string]any{"x": 1},
		},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// satisfy unused-imports linter if some tests get pruned
var _ = bytes.NewReader
