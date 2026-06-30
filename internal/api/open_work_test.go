package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

func TestOpenWorkAPI(t *testing.T) {
	base, tok, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	ctx := context.Background()

	_ = st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"})
	id, _ := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "design", Status: "ACTIVE",
		Title: "open thing", Body: "x",
		Tags: []string{"open", "effort:S", "skill:detective"},
	})
	_, _ = st.RegisterLibrarianInstance(ctx, &store.LibrarianInstance{
		InstanceID: "detective-01", Role: "detective",
	})

	// List
	s, raw := doJSON(t, http.MethodGet, base+"/v1/open_work?role=detective", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}
	var listResp struct {
		Items []struct {
			Entry struct {
				ID string `json:"id"`
			} `json:"Entry"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &listResp)
	if len(listResp.Items) != 1 || listResp.Items[0].Entry.ID != id {
		t.Fatalf("list items: %+v", listResp)
	}

	// Claim
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/claim", tok,
		map[string]any{"role": "detective", "instance_id": "detective-01", "effort": "S"}, nil)
	if s != 201 {
		t.Fatalf("claim: %d", s)
	}
	// Now list shows no open detective work
	s, raw = doJSON(t, http.MethodGet, base+"/v1/open_work?role=detective", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list-after: %d", s)
	}
	_ = json.Unmarshal(raw, &listResp)
	if len(listResp.Items) != 0 {
		t.Fatalf("expected empty post-claim: %+v", listResp)
	}

	// Release
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/release", tok,
		map[string]any{"instance_id": "detective-01"}, nil)
	if s != 204 {
		t.Fatalf("release: %d", s)
	}
	// Back in list
	s, raw = doJSON(t, http.MethodGet, base+"/v1/open_work", tok, nil, nil)
	_ = json.Unmarshal(raw, &listResp)
	if len(listResp.Items) != 1 {
		t.Fatalf("expected back open: %+v", listResp)
	}

	// Re-claim then merge
	doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/claim", tok,
		map[string]any{"role": "detective", "instance_id": "detective-01"}, nil)
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/"+id+"/mark_merged", tok,
		map[string]any{"instance_id": "detective-01", "result": "done"}, nil)
	if s != 204 {
		t.Fatalf("merge: %d", s)
	}
}

func TestOpenWorkAPIValidation(t *testing.T) {
	base, tok, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)

	// Missing fields
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries/x/claim", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-claim: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/x/release", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-release: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/x/mark_merged", tok, map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-merge: %d", s)
	}
	// Bad JSON
	for _, p := range []string{
		"/v1/entries/x/claim", "/v1/entries/x/release", "/v1/entries/x/mark_merged",
	} {
		if got := postRaw(t, http.MethodPost, base+p, tok, "{"); got != 400 {
			t.Fatalf("bad-json %s: %d", p, got)
		}
	}
	// Claim on non-existent entry → 409 (no "open" tag)
	s, _ = doJSON(t, http.MethodPost, base+"/v1/entries/missing/claim", tok,
		map[string]any{"role": "detective", "instance_id": "x"}, nil)
	if s != 409 {
		t.Fatalf("missing-entry claim: %d", s)
	}
}
