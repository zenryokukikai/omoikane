package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

func TestChatThreadsPage(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	// Empty state
	code, body := get(t, srv, "/chat", "")
	if code != 200 {
		t.Fatalf("empty: %d", code)
	}
	if !strings.Contains(string(body), "New thread") {
		t.Fatalf("missing form: %s", string(body)[:300])
	}

	// Seed a thread + message and confirm they render.
	ctx := context.Background()
	tid, err := s.OpenThread(ctx, &store.ChatThread{Title: "from-test", Intent: "observation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: tid, AuthorRole: "human", Content: "hello [[T-AAA]]",
	}); err != nil {
		t.Fatal(err)
	}

	code, body = get(t, srv, "/chat", "")
	if code != 200 || !strings.Contains(string(body), "from-test") {
		t.Fatalf("list: code=%d", code)
	}

	code, body = get(t, srv, "/chat/"+tid, "")
	if code != 200 || !strings.Contains(string(body), "hello") {
		t.Fatalf("thread: code=%d body=%s", code, string(body)[:500])
	}
	// Human message + wiki link both render
	if !strings.Contains(string(body), "chat-msg-human") {
		t.Fatalf("missing human class")
	}
	if !strings.Contains(string(body), `class="wiki"`) {
		t.Fatalf("missing wiki link")
	}
}

// /chat defaults to OPEN threads. CLOSED threads are hidden from the
// default listing — they're the soft-delete bucket. `?status=CLOSED`
// or `?status=all` brings them back. This is the chat-side answer to
// "logical delete": close a thread with a summary like "superseded
// by L-XXX" and it disappears from default view but stays
// reachable.
func TestChatThreadsDefaultHidesClosed(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	ctx := context.Background()

	openID, _ := s.OpenThread(ctx, &store.ChatThread{Title: "alive thread"})
	closedID, _ := s.OpenThread(ctx, &store.ChatThread{Title: "dead thread"})
	_ = s.CloseThread(ctx, closedID, "superseded by L-FAKE")

	// Default → only OPEN visible
	_, body := get(t, srv, "/chat", "")
	bs := string(body)
	if !strings.Contains(bs, "alive thread") {
		t.Errorf("default view missing the OPEN thread:\n%s", bs[:600])
	}
	if strings.Contains(bs, "dead thread") {
		t.Errorf("default view should hide CLOSED thread but it leaked:\n%s", bs[:600])
	}

	// ?status=CLOSED → only CLOSED visible
	_, body = get(t, srv, "/chat?status=CLOSED", "")
	bs = string(body)
	if strings.Contains(bs, "alive thread") {
		t.Errorf("CLOSED filter leaked OPEN thread")
	}
	if !strings.Contains(bs, "dead thread") {
		t.Errorf("CLOSED filter should show CLOSED thread")
	}

	// ?status=all → both visible
	_, body = get(t, srv, "/chat?status=all", "")
	bs = string(body)
	if !strings.Contains(bs, "alive thread") || !strings.Contains(bs, "dead thread") {
		t.Errorf("ALL filter should show both threads")
	}

	_ = openID
}

func TestChatThreadNotFound(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/chat/missing", "")
	if code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestChatNewThreadForm(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	form := url.Values{}
	form.Set("title", "form-thread")
	form.Set("intent", "question")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/chat/new",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noFollowClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "/chat/") {
		t.Fatalf("redirect: %s", resp.Header.Get("Location"))
	}
	// Verify the thread now exists
	threads, _ := s.ListThreads(context.Background(), "", 10)
	found := false
	for _, x := range threads {
		if x.Title == "form-thread" {
			found = true
		}
	}
	if !found {
		t.Fatal("thread not persisted")
	}
}

func TestChatPostMessageForm(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	tid, _ := s.OpenThread(context.Background(), &store.ChatThread{Title: "t"})

	form := url.Values{}
	form.Set("content", "human says hi")
	// no author_role → defaults to human
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/chat/"+tid+"/post",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noFollowClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("post: %d", resp.StatusCode)
	}

	msgs, _ := s.ListChatMessages(context.Background(), tid, 10)
	if len(msgs) != 1 || msgs[0].AuthorRole != "human" || msgs[0].Content != "human says hi" {
		t.Fatalf("msgs: %+v", msgs)
	}

	// Empty content → 400
	form = url.Values{}
	form.Set("content", "")
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/chat/"+tid+"/post",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = noFollowClient().Do(req)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("empty: %d", resp.StatusCode)
	}

	// Bad role → 400 (validator rejects)
	form = url.Values{}
	form.Set("content", "x")
	form.Set("author_role", "wizard")
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/chat/"+tid+"/post",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ = noFollowClient().Do(req)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad role: %d", resp.StatusCode)
	}
}

// When a message is posted through the authed surface, the rendered
// thread page should link the author badge to /u/{author_user_id} so
// clicking it takes you to that user's profile. Locks the end-to-end
// chain: form post → store → render → click-through.
func TestChatAuthorBadgeLinksToProfile(t *testing.T) {
	srv, st, tok := mountAuthed(t) // mounts as alice, auth required
	ctx := context.Background()
	tid, _ := st.OpenThread(ctx, &store.ChatThread{Title: "linkage test"})

	// Post via the dashboard form path — should fill author_user_id
	// from session/token automatically.
	form := url.Values{}
	form.Set("content", "alice writes a line")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/chat/"+tid+"/post?token="+tok,
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post: %d", resp.StatusCode)
	}

	// Store should now know who wrote it.
	msgs, _ := st.ListChatMessages(ctx, tid, 10)
	if len(msgs) != 1 || msgs[0].AuthorUserID != "alice" {
		t.Fatalf("author_user_id not filled: %+v", msgs)
	}

	// Render the thread page and look for the link.
	r2, _ := http.Get(srv.URL + "/chat/" + tid + "?token=" + tok)
	defer r2.Body.Close()
	body, _ := io.ReadAll(r2.Body)
	want := `href="/u/alice`
	if !strings.Contains(string(body), want) {
		t.Errorf("author badge isn't a link to /u/alice: looking for %q in:\n%s",
			want, string(body)[:1500])
	}
}

func TestChatCloseForm(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, true)
	tid, _ := s.OpenThread(context.Background(), &store.ChatThread{Title: "t"})
	form := url.Values{}
	form.Set("summary", "all sorted")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/chat/"+tid+"/close",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ := noFollowClient().Do(req)
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("close: %d", resp.StatusCode)
	}
	threads, _ := s.ListThreads(context.Background(), "CLOSED", 10)
	if len(threads) != 1 || threads[0].Summary != "all sorted" {
		t.Fatalf("threads: %+v", threads)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x") != "x" {
		t.Fatal("first")
	}
	if firstNonEmpty() != "" {
		t.Fatal("empty")
	}
}

// noFollowClient returns an HTTP client that doesn't auto-follow
// 30x — needed to inspect the redirect location.
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
