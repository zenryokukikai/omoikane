//go:build sqlite_fts5

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Chat FTS happy path: post a message containing a distinctive
// phrase, then SearchChatFTS finds it.
func TestSearchChatFTSHappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tid, err := s.OpenThread(ctx, &ChatThread{Title: "search test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PostChatMessage(ctx, &ChatMessage{
		ThreadID:   tid,
		AuthorRole: "human",
		Content:    "discussing the orthogonal teeth jitter loss for run032",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PostChatMessage(ctx, &ChatMessage{
		ThreadID:   tid,
		AuthorRole: "scout",
		Content:    "unrelated content about lipstick color",
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := s.SearchChatFTS(ctx, "orthogonal", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 hit, got %d", len(results))
	}
	if !strings.Contains(results[0].Message.Content, "orthogonal") {
		t.Errorf("wrong content: %s", results[0].Message.Content)
	}
	if results[0].Score == 0 {
		t.Errorf("score should be non-zero")
	}
}

func TestSearchChatFTSEmptyQuery(t *testing.T) {
	s := newTestStore(t)
	_, err := s.SearchChatFTS(context.Background(), "  ", 0)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

// Trigger sync: backfill must cover messages posted BEFORE the
// migration ran. This is implicit in fresh tests (migrations run
// before any insert), but we lock the after-insert path too: a new
// message must appear in FTS immediately.
func TestSearchChatFTSPicksUpNewMessages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tid, _ := s.OpenThread(ctx, &ChatThread{Title: "ttt"})

	_, _ = s.PostChatMessage(ctx, &ChatMessage{
		ThreadID: tid, AuthorRole: "scout",
		Content: "the quick brown fox",
	})
	r1, _ := s.SearchChatFTS(ctx, "brown", 0)
	if len(r1) != 1 {
		t.Fatalf("brown should match 1, got %d", len(r1))
	}

	// Insert a second message, expect to find both with a broader query
	_, _ = s.PostChatMessage(ctx, &ChatMessage{
		ThreadID: tid, AuthorRole: "scout",
		Content: "another fox sighting",
	})
	r2, _ := s.SearchChatFTS(ctx, "fox", 0)
	if len(r2) != 2 {
		t.Fatalf("fox should match 2, got %d", len(r2))
	}
}
