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
