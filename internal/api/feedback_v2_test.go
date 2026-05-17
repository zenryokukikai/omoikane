package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

// seedFeedbackEntry creates a project + one entry and returns the entry ID.
func seedFeedbackEntry(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap",
		Title: "feedback target", Body: "x", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Happy path: POST /v1/feedback returns 201 with the recorded id, and a
// subsequent GET /v1/entries/{id}/engagement reflects the count.
func TestPostFeedbackHappyPath(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedFeedbackEntry(t, st)

	s, raw := doJSON(t, http.MethodPost, base+"/v1/feedback", tok,
		map[string]any{
			"entry_id": id,
			"signal":   "helpful",
			"context":  "applied to fix the run051 NaN",
		}, nil)
	if s != http.StatusCreated {
		t.Fatalf("POST /v1/feedback: %d %s", s, raw)
	}
	var resp struct {
		ID      int64  `json:"id"`
		EntryID string `json:"entry_id"`
		Signal  string `json:"signal"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID == 0 || resp.EntryID != id || resp.Signal != "helpful" {
		t.Errorf("unexpected response: %+v", resp)
	}

	// Engagement view should report 1 helpful.
	s, raw = doJSON(t, http.MethodGet,
		base+"/v1/entries/"+id+"/engagement", tok, nil, nil)
	if s != 200 {
		t.Fatalf("engagement: %d %s", s, raw)
	}
	var eng struct {
		FeedbackHelpful int     `json:"feedback_helpful"`
		EngagementScore float64 `json:"engagement_score"`
	}
	_ = json.Unmarshal(raw, &eng)
	if eng.FeedbackHelpful != 1 {
		t.Errorf("helpful: %d", eng.FeedbackHelpful)
	}
	if eng.EngagementScore < 0.2 || eng.EngagementScore > 0.3 {
		t.Errorf("engagement_score: %v", eng.EngagementScore)
	}
}

// Bad signal: the error response must include the allowed vocabulary
// so an agent can self-correct without a doc lookup.
func TestPostFeedbackBadSignalEchoesAllowed(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedFeedbackEntry(t, st)

	s, raw := doJSON(t, http.MethodPost, base+"/v1/feedback", tok,
		map[string]any{"entry_id": id, "signal": "AWESOME"}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("status: %d %s", s, raw)
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				AllowedSignals []string `json:"allowed_signals"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.Error.Details.AllowedSignals) != 6 {
		t.Errorf("error must echo allowed signals (got %d): %s",
			len(resp.Error.Details.AllowedSignals), raw)
	}
}

func TestPostFeedbackMissingEntryID(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/feedback", tok,
		map[string]any{"signal": "helpful"}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", s, raw)
	}
}

func TestPostFeedbackMissingSignal(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedFeedbackEntry(t, st)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/feedback", tok,
		map[string]any{"entry_id": id}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", s, raw)
	}
}

func TestPostFeedbackNonexistentEntry(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/feedback", tok,
		map[string]any{"entry_id": "L-NONE", "signal": "helpful"}, nil)
	if s != http.StatusNotFound {
		t.Fatalf("want 404, got %d %s", s, raw)
	}
}

func TestPostFeedbackRequiresAuth(t *testing.T) {
	base, _, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/feedback", "",
		map[string]any{"entry_id": "L-X", "signal": "helpful"}, nil)
	if s != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", s)
	}
}

// Search-as-access: a successful /v1/search must log access for each
// returned entry, so a subsequent /v1/entries/{id}/engagement reflects
// reference_count_30d > 0.
func TestSearchAccessLoggedReflectsInEngagement(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "lesson",
		Title: "ferrous narwhal", Body: "narwhals are real", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Do a search that should hit the entry.
	s, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "narwhal"}, nil)
	if s != 200 {
		t.Fatalf("search: %d %s", s, raw)
	}

	// Engagement reflects 1 reference.
	s, raw = doJSON(t, http.MethodGet,
		base+"/v1/entries/"+id+"/engagement", tok, nil, nil)
	if s != 200 {
		t.Fatalf("engagement: %d %s", s, raw)
	}
	var eng struct {
		ReferenceCount30d int `json:"reference_count_30d"`
	}
	_ = json.Unmarshal(raw, &eng)
	if eng.ReferenceCount30d < 1 {
		t.Errorf("search should log access (got %d ref count): %s",
			eng.ReferenceCount30d, raw)
	}
}

// Direct GET /v1/entries/{id} also logs access. Validates the get path
// wiring in entries.go.
func TestEntryGetAccessLogged(t *testing.T) {
	base, tok, st := testServer(t)
	id := seedFeedbackEntry(t, st)
	// Hit the entry directly twice.
	for i := 0; i < 2; i++ {
		s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/"+id, tok, nil, nil)
		if s != 200 {
			t.Fatalf("get %d: %d", i, s)
		}
	}
	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/entries/"+id+"/engagement", tok, nil, nil)
	if s != 200 {
		t.Fatalf("engagement: %d %s", s, raw)
	}
	var eng struct {
		ReferenceCount30d int `json:"reference_count_30d"`
	}
	_ = json.Unmarshal(raw, &eng)
	if eng.ReferenceCount30d < 2 {
		t.Errorf("get x2 should log 2 accesses, got %d", eng.ReferenceCount30d)
	}
}
