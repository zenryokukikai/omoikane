package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /v1/search default behaviour: only entries; no chat_results key.
// Adding `include_chat: true` returns matching chat messages in a
// separate `chat_results` field so existing clients (which read
// `results` only) keep working unchanged.
func TestSearchIncludeChatOptIn(t *testing.T) {
	base, tok, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	ctx := context.Background()

	// Seed: one entry, one chat message, both mentioning "narwhal"
	// so a single query hits both.
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "lesson",
		Title: "narwhal lesson", Status: "ACTIVE",
		Body: "narwhals are real", BodyFormat: "markdown",
		CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := st.OpenThread(ctx, &store.ChatThread{Title: "narwhal thread"})
	_, _ = st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID:   tid,
		AuthorRole: "human",
		Content:    "saw a narwhal at the deploy demo",
	})

	// Default search: only entries
	s, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "narwhal"}, nil)
	if s != 200 {
		t.Fatalf("default: %d %s", s, raw)
	}
	var def struct {
		Results     []any  `json:"results"`
		ChatResults []any  `json:"chat_results"`
	}
	_ = json.Unmarshal(raw, &def)
	if len(def.Results) != 1 {
		t.Errorf("default: want 1 entry, got %d", len(def.Results))
	}
	if def.ChatResults != nil {
		t.Errorf("default must NOT include chat_results (got %d)", len(def.ChatResults))
	}

	// Opt-in: chat results present
	s, raw = doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "narwhal", "include_chat": true}, nil)
	if s != 200 {
		t.Fatalf("with chat: %d %s", s, raw)
	}
	var inc struct {
		Results     []any `json:"results"`
		ChatResults []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"chat_results"`
		ChatCount int `json:"chat_count"`
	}
	if err := json.Unmarshal(raw, &inc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(inc.Results) != 1 {
		t.Errorf("include_chat: want 1 entry, got %d", len(inc.Results))
	}
	if inc.ChatCount != 1 || len(inc.ChatResults) != 1 {
		t.Errorf("include_chat: want 1 chat hit, got %d (%d)", inc.ChatCount, len(inc.ChatResults))
	}
	if inc.ChatResults[0].Message.Content != "saw a narwhal at the deploy demo" {
		t.Errorf("wrong chat hit content: %q", inc.ChatResults[0].Message.Content)
	}
}
