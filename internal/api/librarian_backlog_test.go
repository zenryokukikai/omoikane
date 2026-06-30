package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// End-to-end of the FIFO backlog flow:
//   1. seed 3 entries
//   2. GET /v1/librarian/backlog/next?role=cataloger → returns oldest
//   3. POST /v1/librarian/progress → marks it processed
//   4. GET backlog/next again → returns the second-oldest
//   5. drain remaining; final GET returns 404
func TestBacklogFlowOldestFirst(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := st.CreateEntry(ctx, &store.Entry{
			ProjectID: "p", Type: "trap",
			Title: "e", Body: "x", Status: "ACTIVE",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// 2. first GET — oldest entry
	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next?role=cataloger", tok, nil, nil)
	if s != 200 {
		t.Fatalf("first backlog: %d %s", s, raw)
	}
	var resp struct {
		Entry       map[string]any `json:"entry"`
		BacklogSize int            `json:"backlog_size"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.Entry["id"] != ids[0] {
		t.Errorf("expected oldest %s, got %v", ids[0], resp.Entry["id"])
	}
	if resp.BacklogSize != 3 {
		t.Errorf("backlog_size: %d", resp.BacklogSize)
	}

	// 3. record progress for the first entry
	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/progress", tok,
		map[string]any{
			"role":     "cataloger",
			"entry_id": ids[0],
			"action":   "summarized",
		}, nil)
	if s != http.StatusCreated {
		t.Fatalf("progress: %d %s", s, raw)
	}

	// 4. next GET — second-oldest
	s, raw = doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next?role=cataloger", tok, nil, nil)
	if s != 200 {
		t.Fatalf("second backlog: %d %s", s, raw)
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.Entry["id"] != ids[1] {
		t.Errorf("expected second %s, got %v", ids[1], resp.Entry["id"])
	}
	if resp.BacklogSize != 2 {
		t.Errorf("backlog_size after one processed: %d", resp.BacklogSize)
	}

	// 5. drain remaining
	for _, id := range ids[1:] {
		s, _ = doJSON(t, http.MethodPost, base+"/v1/librarian/progress", tok,
			map[string]any{
				"role": "cataloger", "entry_id": id, "action": "no_action",
			}, nil)
		if s != http.StatusCreated {
			t.Fatalf("drain %s: %d", id, s)
		}
	}
	s, _ = doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next?role=cataloger", tok, nil, nil)
	if s != http.StatusNotFound {
		t.Fatalf("drained backlog should return 404, got %d", s)
	}
}

func TestBacklogNextRejectsBadRole(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next?role=wizard", tok, nil, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", s, raw)
	}
}

func TestBacklogNextMissingRole(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet,
		base+"/v1/librarian/backlog/next", tok, nil, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400 for missing role, got %d", s)
	}
}

// Progress list endpoint returns recent rows for the role.
func TestProgressList(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"})
	id, _ := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap",
		Title: "e", Body: "x", Status: "ACTIVE",
	})
	_, _ = doJSON(t, http.MethodPost, base+"/v1/librarian/progress", tok,
		map[string]any{
			"role": "cataloger", "entry_id": id, "action": "summarized",
		}, nil)

	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/librarian/progress?role=cataloger", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}
	var resp struct {
		Progress []struct {
			EntryID string `json:"entry_id"`
			Action  string `json:"action"`
		} `json:"progress"`
		BacklogSize int `json:"backlog_size"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.Progress) != 1 {
		t.Errorf("expected 1 progress row, got %d", len(resp.Progress))
	}
	if resp.Progress[0].EntryID != id || resp.Progress[0].Action != "summarized" {
		t.Errorf("unexpected progress row: %+v", resp.Progress[0])
	}
	if resp.BacklogSize != 0 {
		t.Errorf("backlog_size should be 0, got %d", resp.BacklogSize)
	}
}
