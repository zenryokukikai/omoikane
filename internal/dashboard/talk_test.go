package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /talk is the per-user Sebastian chat: signed-out shell, own-thread
// history in the sidebar, message rendering, and privacy (a member
// cannot open someone else's thread).
func TestTalkPage(t *testing.T) {
	srv, st, tok := mountAuthed(t) // alice (admin)
	ctx := context.Background()

	// Signed-out (open mount, no session) → login prompt shell.
	openSrv := mount(t, newDashStore(t), true)
	code, body := get(t, openSrv, "/talk", "")
	if code != 200 || !strings.Contains(string(body), "サインイン") {
		t.Fatalf("signed-out shell: code=%d", code)
	}

	// Empty signed-in state.
	code, body = get(t, srv, "/talk", tok)
	if code != 200 || !strings.Contains(string(body), "新しい会話") {
		t.Fatalf("empty talk: code=%d", code)
	}

	// A thread owned by alice with one exchange renders in sidebar+pane;
	// non-ask-sebastian threads stay out of the sidebar.
	tid, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "音声モデルの件", Intent: "ask-sebastian", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "librarian-only", Intent: "observation", CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{ID: "seb", Name: "セバスチャン", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: tid, AuthorRole: "chronicler", AuthorUserID: "seb",
		Content: "お答えいたします"}); err != nil {
		t.Fatal(err)
	}
	// The agent user's avatar_url drives the portrait (not a hardcoded emoji).
	avatar := "https://example.test/seb.png"
	if _, err := st.UpdateUserProfile(ctx, "seb", store.UserProfilePatch{AvatarURL: &avatar}); err != nil {
		t.Fatal(err)
	}
	code, body = get(t, srv, "/talk/"+tid, tok)
	bs := string(body)
	if !strings.Contains(bs, "https://example.test/seb.png") {
		t.Fatalf("sebastian avatar image not rendered")
	}
	if code != 200 || !strings.Contains(bs, "音声モデルの件") ||
		!strings.Contains(bs, "お答えいたします") {
		t.Fatalf("thread view: code=%d", code)
	}
	if strings.Contains(bs, "librarian-only") {
		t.Fatalf("non-ask-sebastian thread leaked into sidebar")
	}
	// Sebastian's message renders on the bot side.
	if !strings.Contains(bs, "talk-msg-bot") {
		t.Fatalf("missing bot bubble class")
	}

	// Privacy: a plain member can't open alice's thread.
	if err := st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	bobTok, err := st.CreateToken(ctx, "bob", "bob", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = get(t, srv, "/talk/"+tid, bobTok)
	if code != 404 {
		t.Fatalf("bob reading alice's thread: code=%d, want 404", code)
	}
}
