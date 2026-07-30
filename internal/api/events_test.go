package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// A comment POSTed while an SSE client is connected must arrive as a
// comment.created event with the entry context (the responder's filter).
func TestEventsStreamDeliversCommentCreated(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()

	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{ID: "bot", Name: "scout", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	eid, err := st.CreateEntry(ctx, &store.Entry{ProjectID: "p", Type: "external_finding",
		Title: "finding", Body: "B", CreatedBy: "bot"})
	if err != nil {
		t.Fatal(err)
	}

	// Open the stream first.
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sreq, _ := http.NewRequestWithContext(sctx, "GET", base+"/v1/events", nil)
	sreq.Header.Set("Authorization", "Bearer "+adminTok)
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", sresp.StatusCode)
	}

	events := make(chan map[string]any, 1)
	go func() {
		sc := bufio.NewScanner(sresp.Body)
		var isComment bool
		for sc.Scan() {
			line := sc.Text()
			if line == "event: comment.created" {
				isComment = true
				continue
			}
			if isComment && strings.HasPrefix(line, "data: ") {
				var d map[string]any
				if json.Unmarshal([]byte(line[len("data: "):]), &d) == nil {
					events <- d
				}
				return
			}
		}
	}()

	// Give the subscription a beat to register, then post the comment.
	time.Sleep(200 * time.Millisecond)
	b, _ := json.Marshal(map[string]string{"body": "question for scout"})
	creq, _ := http.NewRequest("POST", base+"/v1/entries/"+eid+"/comments", bytes.NewReader(b))
	creq.Header.Set("Authorization", "Bearer "+adminTok)
	creq.Header.Set("Content-Type", "application/json")
	cresp, err := http.DefaultClient.Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	cresp.Body.Close()
	if cresp.StatusCode != http.StatusCreated {
		t.Fatalf("comment post = %d", cresp.StatusCode)
	}

	select {
	case d := <-events:
		if d["entry_created_by"] != "bot" || d["entry_title"] != "finding" {
			t.Fatalf("event entry context wrong: %v", d)
		}
		if d["comment"].(map[string]any)["body"] != "question for scout" {
			t.Fatalf("event comment wrong: %v", d)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no comment.created event within 8s")
	}
}

// Thread ownership (?mine=1) and the chat.message SSE event.
func TestChatOwnershipAndStream(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()

	// A second user with its own token opens nothing; admin opens one.
	if err := st.CreateUser(ctx, &store.User{ID: "u2", Name: "u2", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	u2Tok, err := st.CreateToken(ctx, "u2", "u2", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, tok string, body any) *http.Response {
		var br = bytes.NewReader(nil)
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewReader(b)
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

	// Admin opens a thread — created_by must come from the token.
	resp := do("POST", "/v1/librarian/threads", adminTok,
		map[string]string{"title": "ask", "intent": "ask-sebastian"})
	var opened map[string]any
	json.NewDecoder(resp.Body).Decode(&opened)
	resp.Body.Close()
	tid, _ := opened["thread_id"].(string)
	if tid == "" {
		t.Fatalf("no thread_id: %v", opened)
	}
	th, err := st.GetThread(ctx, tid)
	if err != nil || th.CreatedBy != "admin" {
		t.Fatalf("GetThread created_by = %v err=%v", th, err)
	}

	// mine=1 for u2 → empty; for admin → the thread.
	for tok, want := range map[string]int{u2Tok: 0, adminTok: 1} {
		resp = do("GET", "/v1/librarian/threads?mine=1", tok, nil)
		var lst struct {
			Threads []map[string]any `json:"threads"`
		}
		json.NewDecoder(resp.Body).Decode(&lst)
		resp.Body.Close()
		if len(lst.Threads) != want {
			t.Fatalf("mine=1 got %d want %d", len(lst.Threads), want)
		}
	}

	// SSE: chat.message must arrive with thread context.
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sreq, _ := http.NewRequestWithContext(sctx, "GET", base+"/v1/events", nil)
	sreq.Header.Set("Authorization", "Bearer "+adminTok)
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	events := make(chan map[string]any, 1)
	go func() {
		sc := bufio.NewScanner(sresp.Body)
		var isChat bool
		for sc.Scan() {
			line := sc.Text()
			if line == "event: chat.message" {
				isChat = true
				continue
			}
			if isChat && strings.HasPrefix(line, "data: ") {
				var d map[string]any
				if json.Unmarshal([]byte(line[len("data: "):]), &d) == nil {
					events <- d
				}
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	resp = do("POST", "/v1/librarian/chat", adminTok, map[string]string{
		"thread_id": tid, "author_role": "human", "content": "こんにちは"})
	resp.Body.Close()

	select {
	case d := <-events:
		if d["thread_id"] != tid || d["thread_created_by"] != "admin" ||
			d["thread_intent"] != "ask-sebastian" || d["content"] != "こんにちは" {
			t.Fatalf("chat.message payload wrong: %v", d)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no chat.message event within 8s")
	}
}
