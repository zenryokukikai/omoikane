package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
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

// The @mention review-request notification: a comment mentioning the agent
// raises X-Review-Requests on the agent's next call and lists under
// /v1/me/review-requests; resolving it clears the signal.
func TestReviewRequestNotification(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	eid, _ := st.CreateEntry(ctx, &store.Entry{ProjectID: "p", Type: "design", Title: "Auth design", Body: "B"})
	_ = st.CreateUser(ctx, &store.User{ID: "bot", Name: "mac-pi-detective", Role: "agent"})
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

	// admin mentions the bot by user id.
	resp := do("POST", "/v1/entries/"+eid+"/comments", adminTok,
		map[string]any{"body": "please verify", "mentions": []string{"bot"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mention post: %d", resp.StatusCode)
	}
	var c store.EntryComment
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()

	// The bot's next call carries X-Review-Requests: 1.
	resp = do("GET", "/v1/auth/me", botTok, nil)
	if got := resp.Header.Get("X-Review-Requests"); got != "1" {
		t.Fatalf("X-Review-Requests = %q, want 1", got)
	}
	resp.Body.Close()

	// And /v1/me/review-requests lists it with entry context.
	resp = do("GET", "/v1/me/review-requests", botTok, nil)
	var rr struct {
		Total          int `json:"total"`
		ReviewRequests []struct {
			EntryTitle string `json:"entry_title"`
		} `json:"review_requests"`
	}
	json.NewDecoder(resp.Body).Decode(&rr)
	resp.Body.Close()
	if rr.Total != 1 || rr.ReviewRequests[0].EntryTitle != "Auth design" {
		t.Fatalf("review-requests wrong: %+v", rr)
	}

	// The admin (not mentioned) has no review requests.
	resp = do("GET", "/v1/auth/me", adminTok, nil)
	if got := resp.Header.Get("X-Review-Requests"); got != "" {
		t.Fatalf("admin X-Review-Requests = %q, want empty", got)
	}
	resp.Body.Close()

	// Resolving clears the signal.
	resp = do("PATCH", "/v1/comments/"+c.ID, botTok, map[string]bool{"resolved": true})
	resp.Body.Close()
	resp = do("GET", "/v1/auth/me", botTok, nil)
	if got := resp.Header.Get("X-Review-Requests"); got != "" {
		t.Fatalf("after resolve X-Review-Requests = %q, want empty", got)
	}
	resp.Body.Close()
}
