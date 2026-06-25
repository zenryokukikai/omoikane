package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

// End-to-end through the HTTP layer: a human and an agent both comment,
// author identity comes from the token, scopes are enforced.
func TestCommentEndpoints(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()

	// Seed a project + entry to comment on.
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	eid, err := st.CreateEntry(ctx, &store.Entry{ProjectID: "p", Type: "design", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}

	// An agent user + its token (read+write, no admin).
	if err := st.CreateUser(ctx, &store.User{ID: "bot", Name: "mac-pi-scout", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	botTok, _ := st.CreateToken(ctx, "bot", "bot", []string{"read", "write"}, nil)

	do := func(method, path, tok string, body any) *http.Response {
		var br *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewReader(b)
		} else {
			br = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, base+path, br)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Human (admin) posts.
	resp := do("POST", "/v1/entries/"+eid+"/comments", adminTok, map[string]string{"body": "needs a rollback note"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("human post: got %d", resp.StatusCode)
	}
	var c1 store.EntryComment
	json.NewDecoder(resp.Body).Decode(&c1)
	resp.Body.Close()
	if c1.AuthorUserID != "admin" || c1.AuthorKind != "human" {
		t.Fatalf("author not from token: %+v", c1)
	}

	// Agent replies — author kind = agent, derived server-side.
	resp = do("POST", "/v1/entries/"+eid+"/comments", botTok,
		map[string]string{"body": "added the note", "reply_to": c1.ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("agent post: got %d", resp.StatusCode)
	}
	var c2 store.EntryComment
	json.NewDecoder(resp.Body).Decode(&c2)
	resp.Body.Close()
	if c2.AuthorKind != "agent" {
		t.Fatalf("agent author kind wrong: %+v", c2)
	}

	// List shows both.
	resp = do("GET", "/v1/entries/"+eid+"/comments", botTok, nil)
	var list struct {
		Total int `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if list.Total != 2 {
		t.Fatalf("list total = %d, want 2", list.Total)
	}

	// The agent cannot edit the human's comment text (author-only).
	resp = do("PATCH", "/v1/comments/"+c1.ID, botTok, map[string]string{"body": "hijack"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-author edit: got %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// But the agent CAN resolve it (collaborative).
	resp = do("PATCH", "/v1/comments/"+c1.ID, botTok, map[string]bool{"resolved": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve by non-author: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Admin can delete anyone's comment.
	resp = do("DELETE", "/v1/comments/"+c2.ID, adminTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin delete: got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
