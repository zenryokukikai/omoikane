package store

import (
	"context"
	"testing"
)

// Trigram tokenizer: Japanese partial words match without segmentation;
// 1-2 rune tokens fall back to LIKE; mixed queries intersect.
func TestJapaneseSearchRecall(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	mk := func(title, body string) string {
		id, err := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "design", Title: title, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	target := mk("進捗メモ", "一番アクティブなプロジェクトはこれです。接続がステートレス方向へ正式化されました。")
	other := mk("別件", "音声合成モデルの評価メモ。")

	find := func(q string) []string {
		res, _, err := s.SearchFTS(ctx, q, EntryFilter{})
		if err != nil {
			t.Fatalf("SearchFTS(%q): %v", q, err)
		}
		ids := []string{}
		for _, r := range res {
			ids = append(ids, r.Entry.ID)
		}
		return ids
	}
	has := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}

	// Japanese partial word inside an unsegmented run (previously 0 hits).
	if ids := find("ステートレス"); !has(ids, target) {
		t.Fatalf("ステートレス should hit %s, got %v", target, ids)
	}
	if ids := find("アクティブ プロジェクト"); !has(ids, target) {
		t.Fatalf("multi-token Japanese should hit %s, got %v", target, ids)
	}
	// 2-rune token → LIKE fallback.
	if ids := find("音声"); !has(ids, other) {
		t.Fatalf("2-rune 音声 should hit %s via LIKE fallback, got %v", other, ids)
	}
	// Mixed long+short intersects.
	if ids := find("音声 モデル"); !has(ids, other) || has(ids, target) {
		t.Fatalf("mixed query wrong: %v", ids)
	}
	// English still works (substring now, superset of before).
	mkEN := mk("English note", "The retry backoff logic was wrong.")
	if ids := find("backoff"); !has(ids, mkEN) {
		t.Fatalf("english recall broken: %v", ids)
	}

	// Chat side: Japanese partial + short fallback.
	if err := s.CreateUser(ctx, &User{ID: "u1", Name: "u1", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	tid, err := s.OpenThread(ctx, &ChatThread{Title: "t", CreatedBy: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostChatMessage(ctx, &ChatMessage{ThreadID: tid, AuthorRole: "human",
		Content: "MCPの接続がステートレス化された件です。音声も関係します。"}); err != nil {
		t.Fatal(err)
	}
	cres, err := s.SearchChatFTS(ctx, "ステートレス", 10)
	if err != nil || len(cres) == 0 {
		t.Fatalf("chat ステートレス: err=%v n=%d", err, len(cres))
	}
	cres, err = s.SearchChatFTS(ctx, "音声", 10)
	if err != nil || len(cres) == 0 {
		t.Fatalf("chat 音声 LIKE fallback: err=%v n=%d", err, len(cres))
	}
}
