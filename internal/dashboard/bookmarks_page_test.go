package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /bookmarks renders the shortlist; the entry page shows the toggle
// with the saved state.
func TestBookmarksPage(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	eid, err := st.CreateEntry(ctx, &store.Entry{ProjectID: "p", Type: "design", Title: "保存対象", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}

	// Empty state.
	code, body := get(t, srv, "/bookmarks", tok)
	if code != 200 || !strings.Contains(string(body), "まだブックマークがありません") {
		t.Fatalf("empty: code=%d", code)
	}
	// Entry page shows the (off) toggle.
	_, body = get(t, srv, "/entries/"+eid, tok)
	if !strings.Contains(string(body), "🔖 ブックマーク") {
		t.Fatalf("toggle missing on entry page")
	}

	if err := st.AddBookmark(ctx, "alice", eid); err != nil {
		t.Fatal(err)
	}
	code, body = get(t, srv, "/bookmarks", tok)
	if code != 200 || !strings.Contains(string(body), "保存対象") {
		t.Fatalf("list: code=%d", code)
	}
	_, body = get(t, srv, "/entries/"+eid, tok)
	if !strings.Contains(string(body), "🔖 保存済み") {
		t.Fatalf("toggle not showing saved state")
	}
}
