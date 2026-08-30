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
	// #131: the past-thread list is wrapped in a <details> disclosure so it
	// collapses into a tap-to-open menu on narrow screens (server can only
	// pin STRUCTURE — the desktop/mobile split is pure CSS). The thread
	// titles must live INSIDE that details, and the ＋新しい会話 link must
	// stay OUTSIDE it (always-visible導線). Verify the markup order:
	// talk-new … <details class="talk-side-menu"> … talk-thread … </details>.
	sideMenu := strings.Index(bs, `class="talk-side-menu"`)
	newLink := strings.Index(bs, `class="talk-new`)
	// The sidebar thread entries (class="talk-thread…") are the only place
	// "talk-thread" appears; they must sit AFTER the details opens (inside
	// it). The thread title itself is unusable for ordering — it also lands
	// in the page <title> (talk.go:98), which precedes the sidebar.
	threadList := strings.Index(bs, "talk-thread")
	if sideMenu < 0 {
		t.Fatalf("thread list not wrapped in a talk-side-menu <details>")
	}
	if newLink < 0 || newLink > sideMenu {
		t.Fatalf("＋新しい会話 link must render before (outside) the thread menu")
	}
	if threadList < 0 || threadList < sideMenu {
		t.Fatalf("thread entries must render inside the talk-side-menu details")
	}
	// The collapsed-state orientation bar exists (label + count).
	if !strings.Contains(bs, `class="talk-side-bar"`) {
		t.Fatalf("thread menu missing its summary bar")
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

// The header nav names the viewer's own librarian (#73 UX): with an
// active librarian every page's nav says 🤖 <name>; without one it says
// the default responder.
func TestNavShowsOwnLibrarianName(t *testing.T) {
	srv, st, tok := mountLibrarian(t, &fakeProvisioner{})
	ctx := context.Background()
	// Before configuring: default label.
	_, body := get(t, srv, "/entries", tok)
	if strings.Contains(string(body), "🤖 きりんテスト") {
		t.Fatalf("librarian name shown before configuration")
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "きりんテスト", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, srv, "/entries", tok)
	if !strings.Contains(string(body), "🤖 きりんテスト") {
		t.Fatalf("nav must show the viewer's librarian name on every page")
	}
}

// Personal-librarian identity on /talk (issue #73 slice B): the
// librarian posts with its OWNER's token, so author_user_id alone can
// no longer decide the bubble side — author_role must. And when the
// thread owner has a librarian, the respondent identity (header name,
// avatar, per-bubble icon) is the librarian's, not the default
// responder's.
func TestTalkPersonalLibrarianIdentity(t *testing.T) {
	// Feature ON (h.Librarian set) — identity resolution is gated on it.
	srv, st, tok := mountLibrarian(t, &fakeProvisioner{}) // alice (admin)
	ctx := context.Background()

	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "アイ", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	tid, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "司書との会話", Intent: "talk", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	hid, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: tid, AuthorRole: "human", AuthorUserID: "alice",
		Content: "質問です"})
	if err != nil {
		t.Fatal(err)
	}
	// The impersonation case: assistant reply carrying alice's own user
	// id (the librarian holds alice's token).
	aid, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: tid, AuthorRole: "assistant", AuthorUserID: "alice",
		Content: "回答です"})
	if err != nil {
		t.Fatal(err)
	}

	code, body := get(t, srv, "/talk/"+tid, tok)
	bs := string(body)
	if code != 200 {
		t.Fatalf("thread view: code=%d", code)
	}
	// Bubble sides are decided by role+user, not user alone.
	if !strings.Contains(bs, `talk-msg-me" data-mid="`+hid) {
		t.Fatalf("human message not on my side")
	}
	if !strings.Contains(bs, `talk-msg-bot" data-mid="`+aid) {
		t.Fatalf("assistant reply posted with the owner's token rendered on the owner's side")
	}
	// Respondent identity is the librarian's.
	if !strings.Contains(bs, "アイ") || !strings.Contains(bs, "🤖") {
		t.Fatalf("librarian name/icon missing from thread view")
	}
	if strings.Contains(bs, "template error:") {
		t.Fatalf("page contains a template execution error")
	}
	// Live-append fragments carry the same identity (the 🤖 bubble icon).
	code, body = get(t, srv, "/talk/"+tid+"?frag=since&cursor="+hid, tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, `talk-msg-bot" data-mid="`+aid) || !strings.Contains(bs, "🤖") {
		t.Fatalf("frag identity wrong: code=%d", code)
	}
	// New-conversation view: the viewer's own librarian fronts the page.
	code, body = get(t, srv, "/talk", tok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "アイ") {
		t.Fatalf("librarian name missing from /talk shell: code=%d", code)
	}

	// A user WITHOUT a librarian keeps the default responder identity.
	if err := st.CreateUser(ctx, &store.User{ID: "carol", Name: "Carol", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	carolTok, err := st.CreateToken(ctx, "carol", "c", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, body = get(t, srv, "/talk", carolTok)
	bs = string(body)
	if code != 200 || !strings.Contains(bs, "コンシェルジュ") || strings.Contains(bs, "アイ") {
		t.Fatalf("default responder identity broken for librarian-less user: code=%d", code)
	}
}

// #126: with the runtime unconfigured (no OPENCRAB_URL → h.Librarian
// nil) an ACTIVE user_librarians row still fronts /talk — BOTH the header
// nav AND the in-page respondent (talk-head name + avatar). The librarian
// identity is a display fact keyed on the row's status, decoupled from
// the opencrab PROVISIONING capability; who answers the thread is decided
// by the dispatch layer, not by whether this process can provision. This
// pins the corrected §25.7 reasoning so the old h.Librarian gate is not
// restored.
func TestTalkLibrarianIdentityWithoutRuntime(t *testing.T) {
	srv, st, tok := mountAuthed(t) // alice (admin), h.Librarian NOT set
	ctx := context.Background()

	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "きりん", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	tid, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "runtime off", Intent: "talk", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/talk", "/talk/" + tid} {
		code, body := get(t, srv, path, tok)
		bs := string(body)
		if code != 200 {
			t.Fatalf("%s: code=%d", path, code)
		}
		// The in-page respondent identity is the librarian's, not the
		// default responder's: talk-head name + the 🤖 librarian glyph
		// (IconText default, no uploaded icon here).
		if !strings.Contains(bs, `talk-name">きりん`) {
			t.Fatalf("%s: librarian should front the respondent with the runtime off", path)
		}
		if !strings.Contains(bs, `talk-avatar">🤖`) {
			t.Fatalf("%s: librarian avatar missing from the respondent head", path)
		}
		if strings.Contains(bs, `talk-name">コンシェルジュ`) {
			t.Fatalf("%s: default responder still fronts the page despite an active row", path)
		}
		// And the header nav names the entry point after the same row.
		if !strings.Contains(bs, `class="nav-journal"`) || !strings.Contains(bs, "きりん</a>") {
			t.Fatalf("%s: header nav should show the personal librarian regardless of the runtime", path)
		}
	}
}

// The genuine fallback stays intact: with no active user_librarians row
// the /talk respondent AND the nav are the default responder, runtime off.
func TestTalkDefaultResponderWithoutLibrarian(t *testing.T) {
	srv, s, tok := mountAuthed(t) // alice, no user_librarians row
	ctx := context.Background()
	tid, err := s.OpenThread(ctx, &store.ChatThread{
		Title: "no librarian", Intent: "talk", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/talk", "/talk/" + tid} {
		code, body := get(t, srv, path, tok)
		bs := string(body)
		if code != 200 {
			t.Fatalf("%s: code=%d", path, code)
		}
		if !strings.Contains(bs, `talk-name">コンシェルジュ`) {
			t.Fatalf("%s: default responder identity missing from talk-head", path)
		}
		if strings.Contains(bs, `talk-avatar">🤖`) {
			t.Fatalf("%s: librarian avatar shown with no librarian row", path)
		}
	}
}
