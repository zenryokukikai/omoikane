package dashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /talk is the per-user responder chat: signed-out shell, own-thread
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
	// non-talk-intent threads stay out of the sidebar.
	tid, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "音声モデルの件", Intent: "talk", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "librarian-only", Intent: "observation", CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{ID: "seb", Name: "コンシェルジュ", Role: "agent"}); err != nil {
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
		t.Fatalf("non-talk thread leaked into sidebar")
	}
	// The responder's message renders on the bot side.
	if !strings.Contains(bs, "talk-msg-bot") {
		t.Fatalf("missing bot bubble class")
	}
	// The live listener must clear the pending line when a responder
	// message arrives — the defence for responders that never send
	// chat.status done (#36). Guarded on author_role so the asker's own
	// message keeps 考えております….
	if !strings.Contains(bs, "d.author_role !== 'human') talkPending(false)") {
		t.Fatalf("chat.message listener missing responder pending-clear guard")
	}

	// Virtualized window (#45): with 55 messages the page renders only
	// the newest 50; the older ones arrive via ?frag=before, live
	// updates via ?frag=since.
	vt, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "長い会話", Intent: "talk", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 1; i <= 55; i++ {
		id, err := st.PostChatMessage(ctx, &store.ChatMessage{
			ThreadID: vt, AuthorRole: "human", AuthorUserID: "alice",
			Content: fmt.Sprintf("メッセージ番号%03d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	code, body = get(t, srv, "/talk/"+vt, tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "メッセージ番号055") || !strings.Contains(bs, "メッセージ番号006") {
		t.Fatalf("window must include the newest 50: code=%d", code)
	}
	if strings.Contains(bs, "メッセージ番号005") {
		t.Fatalf("messages beyond the window leaked into the initial render")
	}
	if !strings.Contains(bs, `data-mid=`) || !strings.Contains(bs, "talk-top-sentinel") {
		t.Fatalf("virtualization hooks (data-mid / sentinel) missing")
	}
	// A template execution error is written INTO the page after partial
	// output (200 + content), so content assertions alone can pass while
	// the tail of the layout — including the timezone localizer script —
	// silently never renders. Guard the full render explicitly.
	if strings.Contains(bs, "template error:") {
		t.Fatalf("page contains a template execution error")
	}
	if !strings.Contains(bs, "MutationObserver") {
		t.Fatalf("layout localizer script missing from rendered page")
	}
	// Upward page: everything strictly older than the oldest rendered.
	code, body = get(t, srv, "/talk/"+vt+"?frag=before&cursor="+ids[5], tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "メッセージ番号001") || !strings.Contains(bs, "メッセージ番号005") {
		t.Fatalf("frag=before window wrong: code=%d", code)
	}
	if strings.Contains(bs, "メッセージ番号006") || strings.Contains(bs, `data-has-more="1"`) {
		t.Fatalf("frag=before must exclude the cursor row and report no further history")
	}
	// Live append: only what is newer than the cursor.
	code, body = get(t, srv, "/talk/"+vt+"?frag=since&cursor="+ids[52], tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "メッセージ番号054") || !strings.Contains(bs, "メッセージ番号055") ||
		strings.Contains(bs, "メッセージ番号052") {
		t.Fatalf("frag=since window wrong: code=%d", code)
	}
	// tail: the cursorless newest window — live-update recovery for an
	// empty-rendered thread (#57-4). No cursor required.
	code, body = get(t, srv, "/talk/"+vt+"?frag=tail&cursor=", tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "メッセージ番号055") || !strings.Contains(bs, "メッセージ番号006") ||
		strings.Contains(bs, "メッセージ番号005") {
		t.Fatalf("frag=tail window wrong: code=%d", code)
	}
	// A cursor from another thread must not leak messages.
	if code, _ = get(t, srv, "/talk/"+tid+"?frag=before&cursor="+ids[5], tok); code != 400 {
		t.Fatalf("cross-thread cursor: code=%d, want 400", code)
	}
	if code, _ = get(t, srv, "/talk/"+vt+"?frag=nope&cursor="+ids[5], tok); code != 400 {
		t.Fatalf("bad frag mode: code=%d, want 400", code)
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
