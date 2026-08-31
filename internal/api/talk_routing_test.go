package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/config"
	"github.com/zenryokukikai/omoikane/internal/enrich"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// testServerWithTalkDispatch is testServer plus a TalkDispatcher wired
// into the webhook dispatcher (issue #73 slice B).
func testServerWithTalkDispatch(t *testing.T, td TalkDispatcher) (base, tok string, st *store.Store) {
	t.Helper()
	return testServerWithTalkDispatchOpts(t, td, nil)
}

// testServerWithTalkDispatchOpts additionally lets the caller adjust
// the Handler before Mount (e.g. the GATE_TALK_REST_FORCE kill switch).
func testServerWithTalkDispatchOpts(t *testing.T, td TalkDispatcher, mutate func(*Handler)) (base, tok string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "admin", Name: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, err = st.CreateToken(context.Background(), "admin", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store:        st,
		Enricher:     enrich.New("", "", "", "", logger),
		SecretsMode:  config.SecretsEnforce,
		Logger:       logger,
		TalkDispatch: td,
	}
	if mutate != nil {
		mutate(h)
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(logger))
	r.Use(Audit(st, logger))
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, tok, st
}

// The author_role vocabulary accepts "assistant" (the off-roster
// personal librarian, #73) — the instructions template tells the agent
// to post with exactly this role, so a 400 here would silence every
// personal librarian.
func TestChatAuthorAssistant(t *testing.T) {
	base, tok, _ := testServer(t)
	_, th := doJSONMap(t, "POST", base+"/v1/librarian/threads", tok,
		map[string]string{"title": "assistant vocab", "intent": "talk"})
	tid, _ := th["thread_id"].(string)
	if tid == "" {
		t.Fatalf("no thread id: %v", th)
	}
	code, out := doJSONMap(t, "POST", base+"/v1/librarian/chat", tok, map[string]string{
		"thread_id": tid, "author_role": "assistant", "content": "お答えします"})
	if code != http.StatusCreated {
		t.Fatalf("assistant chat post: %d %v", code, out)
	}
	// Roster stays closed: assistant is a chat author, never a
	// registrable librarian role.
	if store.ValidLibrarianRole("assistant") {
		t.Fatalf("assistant leaked into the librarian-role vocabulary")
	}
}

// Routing matrix (issue #73 slice B): a human /talk message goes to the
// thread owner's personal librarian on the opencrab runtime — and NOT
// to the webhook-subscribed default responder. Owners without an
// (active) librarian keep the webhook path unchanged, and assistant
// replies never bounce back into either pipe. Since issue #134 the REST
// lane also delivers: the runtime's response content is posted back
// into the thread as the assistant, followed by the done chat.status.
func TestTalkRoutingPersonalLibrarian(t *testing.T) {
	const crabReply = "調査しました。答えは42です。"
	// Mock runtime: capture path + body of every messages call.
	var crabCalls atomic.Int32
	var crabPath, crabBody atomic.Value
	crab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		crabPath.Store(r.URL.Path)
		crabBody.Store(string(b))
		crabCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"s","responses":[{"agent_id":"a","content":"` + crabReply + `"}]}`))
	}))
	defer crab.Close()

	// The REAL client — the request shape the runtime sees is part of
	// the contract under test.
	oc := opencrab.New(crab.URL, "http://kb.test")
	var events <-chan Event
	base, adminTok, st := testServerWithTalkDispatchOpts(t, oc, func(h *Handler) {
		// Tap the event bus before Mount starts the dispatcher, so the
		// done broadcast of the fallback delivery is observable.
		h.Events = NewEventBus()
		events, _ = h.Events.Subscribe(nil)
	})
	ctx := context.Background()

	// Webhook mock: the default responder's inbox.
	var hookCalls atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookCalls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hook.Close()
	if code, out := doJSONMap(t, "POST", base+"/v1/admin/webhooks", adminTok,
		map[string]any{"url": hook.URL, "event_types": []string{"chat.message"}}); code != http.StatusCreated {
		t.Fatalf("create webhook: %d %v", code, out)
	}

	// u1 has an active personal librarian; u2 has a disabled one.
	for _, u := range []string{"u1", "u2"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "u1", AgentID: "plib-u1", Name: "アイ", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "u2", AgentID: "plib-u2", Name: "ロク", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	u1Tok, err := st.CreateToken(ctx, "u1", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u2Tok, err := st.CreateToken(ctx, "u2", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	openTalk := func(tok, title string) string {
		t.Helper()
		code, th := doJSONMap(t, "POST", base+"/v1/librarian/threads", tok,
			map[string]string{"title": title, "intent": "talk"})
		if code != http.StatusCreated {
			t.Fatalf("open thread: %d %v", code, th)
		}
		tid, _ := th["thread_id"].(string)
		return tid
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s", what)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// 1. Librarian-holding owner → runtime, not webhook.
	tid1 := openTalk(u1Tok, "経路テスト")
	if code, out := doJSONMap(t, "POST", base+"/v1/librarian/chat", u1Tok, map[string]string{
		"thread_id": tid1, "author_role": "human", "content": "こんにちは司書さん"}); code != http.StatusCreated {
		t.Fatalf("u1 human post: %d %v", code, out)
	}
	waitFor("runtime dispatch", func() bool { return crabCalls.Load() >= 1 })
	if p, _ := crabPath.Load().(string); p != "/api/agents/plib-u1/messages" {
		t.Fatalf("runtime path: %q", p)
	}
	var msg struct {
		UserID  string `json:"user_id"`
		Content string `json:"content"`
	}
	body, _ := crabBody.Load().(string)
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("runtime body not JSON: %q", body)
	}
	// Issue #137: the caller identity handed to the runtime is the
	// librarian's OWNER (its kb user id), not a deployment-wide
	// constant — it must match the trust row provisioning wrote, or the
	// owner-gated tools resolve to a stranger's privileges.
	if msg.UserID != "u1" {
		t.Fatalf("runtime caller id: %q, want the librarian owner's kb user id u1", msg.UserID)
	}
	// The human text (and the thread title as context) must be in the
	// content. The thread id and the posting-recipe wording must NOT:
	// since #132/#134 the server delivers the reply itself, and handing
	// the agent a thread_id invites a double-post.
	if !strings.Contains(msg.Content, "こんにちは司書さん") || !strings.Contains(msg.Content, "経路テスト") {
		t.Fatalf("dispatch content missing body or title: %q", msg.Content)
	}
	for _, banned := range []string{tid1, "thread_id", "レシピ"} {
		if strings.Contains(msg.Content, banned) {
			t.Fatalf("dispatch content still hands the agent %q (issue #134): %q", banned, msg.Content)
		}
	}

	// Issue #134: the runtime's response content is delivered into the
	// thread as the librarian's (assistant) reply.
	waitFor("assistant reply delivery", func() bool {
		return len(threadMessages(t, base, u1Tok, tid1)) >= 2
	})
	msgs := threadMessages(t, base, u1Tok, tid1)
	last := msgs[len(msgs)-1]
	if last["author_role"] != "assistant" || last["content"] != crabReply {
		t.Fatalf("delivered reply = %v, want assistant %q", last, crabReply)
	}
	if last["author_user_id"] != "u1" {
		t.Fatalf("reply attributed to %v, want the thread owner u1 (gateway-lane author semantics)", last["author_user_id"])
	}
	// ... followed by the terminal chat.status {done:true} broadcast the
	// /talk UI clears its pending indicator on.
	waitFor("done chat.status broadcast", func() bool {
		for {
			select {
			case e := <-events:
				if e.Type != "chat.status" {
					continue
				}
				d, _ := e.Data.(map[string]any)
				if d["thread_id"] == tid1 && d["done"] == true {
					return true
				}
			default:
				return false
			}
		}
	})
	time.Sleep(300 * time.Millisecond)
	if hookCalls.Load() != 0 {
		t.Fatalf("webhook received a message that was routed to the personal librarian")
	}

	// 2. Assistant reply (posted with the owner's token) → neither pipe.
	if code, _ := doJSONMap(t, "POST", base+"/v1/librarian/chat", u1Tok, map[string]string{
		"thread_id": tid1, "author_role": "assistant", "content": "お答えします"}); code != http.StatusCreated {
		t.Fatalf("assistant post failed")
	}
	time.Sleep(300 * time.Millisecond)
	if crabCalls.Load() != 1 || hookCalls.Load() != 0 {
		t.Fatalf("assistant reply leaked into a delivery pipe (crab=%d hook=%d)",
			crabCalls.Load(), hookCalls.Load())
	}

	// 3. Owner without a librarian row → webhook as before, runtime silent.
	tidA := openTalk(adminTok, "既定応答者")
	if code, _ := doJSONMap(t, "POST", base+"/v1/librarian/chat", adminTok, map[string]string{
		"thread_id": tidA, "author_role": "human", "content": "セバスチャンへ"}); code != http.StatusCreated {
		t.Fatalf("admin human post failed")
	}
	waitFor("webhook delivery (no librarian)", func() bool { return hookCalls.Load() >= 1 })
	if crabCalls.Load() != 1 {
		t.Fatalf("runtime called for an owner without a librarian")
	}

	// 4. Disabled librarian row → webhook, runtime silent.
	tid2 := openTalk(u2Tok, "無効化済み")
	if code, _ := doJSONMap(t, "POST", base+"/v1/librarian/chat", u2Tok, map[string]string{
		"thread_id": tid2, "author_role": "human", "content": "いますか"}); code != http.StatusCreated {
		t.Fatalf("u2 human post failed")
	}
	waitFor("webhook delivery (disabled librarian)", func() bool { return hookCalls.Load() >= 2 })
	if crabCalls.Load() != 1 {
		t.Fatalf("runtime called for a disabled librarian")
	}
}

// fakeTalkDispatcher records every DispatchTalk call (issue #104
// cutover guard tests) and answers a canned reply/err (issue #134
// fallback-delivery tests). Zero value = empty reply, no error.
type fakeTalkDispatcher struct {
	mu       sync.Mutex
	calls    []string // agent ids, in dispatch order
	owners   []string // owner user ids handed to the runtime, same order
	contents []string // dispatched framing+body, same order
	reply    string   // returned as the agent's reply
	err      error    // returned as the dispatch error
}

func (f *fakeTalkDispatcher) DispatchTalk(_ context.Context, agentID, ownerUserID, content string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentID)
	f.owners = append(f.owners, ownerUserID)
	f.contents = append(f.contents, content)
	return f.reply, f.err
}

func (f *fakeTalkDispatcher) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Gateway-cutover guard (issue #104): a /talk message on a thread that
// the gateway carries (librarian has gate_instance_id AND the thread
// has a binding row) is claimed — no REST dispatch, no webhook default
// responder. Everything else keeps the pre-cutover behaviour: unbound
// threads and gate-less librarians REST-dispatch, librarian-less owners
// fall through to the webhook.
func TestTalkGatewayCutoverGuard(t *testing.T) {
	fake := &fakeTalkDispatcher{}
	base, adminTok, st := testServerWithTalkDispatch(t, fake)
	ctx := context.Background()

	// Webhook mock: the default responder's inbox.
	var hookCalls atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookCalls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hook.Close()
	if code, out := doJSONMap(t, "POST", base+"/v1/admin/webhooks", adminTok,
		map[string]any{"url": hook.URL, "event_types": []string{"chat.message"}}); code != http.StatusCreated {
		t.Fatalf("create webhook: %d %v", code, out)
	}

	// u1: active librarian behind the gate; u2: active librarian, no gate.
	for _, u := range []string{"u1", "u2"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "u1", AgentID: "plib-u1", Name: "アイ", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserLibrarianGateInstance(ctx, "u1", "inst-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "u2", AgentID: "plib-u2", Name: "ロク", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	u1Tok, err := st.CreateToken(ctx, "u1", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u2Tok, err := st.CreateToken(ctx, "u2", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	openTalk := func(tok, title string) string {
		t.Helper()
		code, th := doJSONMap(t, "POST", base+"/v1/librarian/threads", tok,
			map[string]string{"title": title, "intent": "talk"})
		if code != http.StatusCreated {
			t.Fatalf("open thread: %d %v", code, th)
		}
		tid, _ := th["thread_id"].(string)
		return tid
	}
	post := func(tok, tid, text string) {
		t.Helper()
		if code, out := doJSONMap(t, "POST", base+"/v1/librarian/chat", tok, map[string]string{
			"thread_id": tid, "author_role": "human", "content": text}); code != http.StatusCreated {
			t.Fatalf("human post: %d %v", code, out)
		}
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for %s", what)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// 1. Gate instance + binding row → claimed by the gateway path.
	tidBound := openTalk(u1Tok, "gateway 経路")
	if err := st.PutTalkGateBinding(ctx, tidBound, "b-1", "inst-1"); err != nil {
		t.Fatal(err)
	}
	post(u1Tok, tidBound, "gateway に乗るはず")

	// 2. Gate instance, NO binding row (pre-cutover thread) → REST.
	tidUnbound := openTalk(u1Tok, "cutover 前スレッド")
	post(u1Tok, tidUnbound, "RESTのまま")

	// 3. Binding row but NO gate instance → REST (a stray binding row
	// alone must never claim).
	tidNoGate := openTalk(u2Tok, "gateなし司書")
	if err := st.PutTalkGateBinding(ctx, tidNoGate, "b-2", "inst-x"); err != nil {
		t.Fatal(err)
	}
	post(u2Tok, tidNoGate, "こちらもREST")

	// 4. No librarian at all → webhook fall-through unchanged.
	tidNone := openTalk(adminTok, "既定応答者")
	post(adminTok, tidNone, "セバスチャンへ")

	// The dispatcher consumes events serially, so once the LAST
	// message's webhook delivery is observed, every earlier message
	// has been routed.
	waitFor("webhook delivery (no librarian)", func() bool { return hookCalls.Load() >= 1 })
	waitFor("REST dispatches", func() bool { return len(fake.snapshot()) >= 2 })
	time.Sleep(300 * time.Millisecond) // settle: catch any extra call

	calls := fake.snapshot()
	if len(calls) != 2 {
		t.Fatalf("REST dispatch calls = %v, want exactly [plib-u1 plib-u2] in some order", calls)
	}
	seen := map[string]bool{calls[0]: true, calls[1]: true}
	if !seen["plib-u1"] || !seen["plib-u2"] {
		t.Fatalf("REST dispatch went to %v, want one call each for plib-u1 (unbound thread) and plib-u2 (no gate instance)", calls)
	}
	if hookCalls.Load() != 1 {
		t.Fatalf("webhook calls = %d, want exactly 1 (the librarian-less thread only)", hookCalls.Load())
	}
}

// GATE_TALK_REST_FORCE wins over an existing binding: with the kill
// switch on, a fully gateway-bound thread still REST-dispatches.
func TestTalkGatewayCutoverKillSwitch(t *testing.T) {
	fake := &fakeTalkDispatcher{}
	base, _, st := testServerWithTalkDispatchOpts(t, fake, func(h *Handler) {
		h.GateTalkRESTForce = true
	})
	ctx := context.Background()

	if err := st.CreateUser(ctx, &store.User{ID: "u1", Name: "u1", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "u1", AgentID: "plib-u1", Name: "アイ", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserLibrarianGateInstance(ctx, "u1", "inst-1"); err != nil {
		t.Fatal(err)
	}
	u1Tok, err := st.CreateToken(ctx, "u1", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	code, th := doJSONMap(t, "POST", base+"/v1/librarian/threads", u1Tok,
		map[string]string{"title": "kill switch", "intent": "talk"})
	if code != http.StatusCreated {
		t.Fatalf("open thread: %d %v", code, th)
	}
	tid, _ := th["thread_id"].(string)
	if err := st.PutTalkGateBinding(ctx, tid, "b-1", "inst-1"); err != nil {
		t.Fatal(err)
	}
	if code, out := doJSONMap(t, "POST", base+"/v1/librarian/chat", u1Tok, map[string]string{
		"thread_id": tid, "author_role": "human", "content": "強制REST"}); code != http.StatusCreated {
		t.Fatalf("human post: %d %v", code, out)
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(fake.snapshot()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timeout: kill switch did not force REST dispatch")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0] != "plib-u1" {
		t.Fatalf("REST dispatch calls = %v, want [plib-u1]", calls)
	}
}

// REST-fallback suppression and error handling (issue #134): an empty
// reply, the NO_REPLY sentinel (trimmed match — opencrab's own rule)
// and a dispatch error all deliver NOTHING into the thread. In
// particular an error must never appear as the librarian's words.
func TestTalkRESTFallbackSuppression(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		err   error
	}{
		{"empty reply", "", nil},
		{"NO_REPLY sentinel", "NO_REPLY", nil},
		{"NO_REPLY with whitespace", "  NO_REPLY\n", nil},
		{"dispatch error", "こわれた応答", errors.New("runtime turn failed: (Error: boom)")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTalkDispatcher{reply: tc.reply, err: tc.err}
			base, _, st := testServerWithTalkDispatch(t, fake)
			ctx := context.Background()

			if err := st.CreateUser(ctx, &store.User{ID: "u1", Name: "u1", Role: "member"}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
				UserID: "u1", AgentID: "plib-u1", Name: "アイ", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			u1Tok, err := st.CreateToken(ctx, "u1", "t", []string{"read", "write"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			code, th := doJSONMap(t, "POST", base+"/v1/librarian/threads", u1Tok,
				map[string]string{"title": "抑止テスト", "intent": "talk"})
			if code != http.StatusCreated {
				t.Fatalf("open thread: %d %v", code, th)
			}
			tid, _ := th["thread_id"].(string)
			if code, out := doJSONMap(t, "POST", base+"/v1/librarian/chat", u1Tok, map[string]string{
				"thread_id": tid, "author_role": "human", "content": "いますか"}); code != http.StatusCreated {
				t.Fatalf("human post: %d %v", code, out)
			}

			deadline := time.Now().Add(5 * time.Second)
			for len(fake.snapshot()) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("timeout waiting for REST dispatch")
				}
				time.Sleep(25 * time.Millisecond)
			}
			time.Sleep(300 * time.Millisecond) // settle: delivery would land here
			msgs := threadMessages(t, base, u1Tok, tid)
			if len(msgs) != 1 {
				t.Fatalf("thread messages = %v, want the human message only (suppressed/failed reply must not post)", msgs)
			}
			if msgs[0]["author_role"] != "human" {
				t.Fatalf("unexpected message: %v", msgs[0])
			}
		})
	}
}

// The dispatch framing (issue #134): tells the agent its response body
// is delivered to the user as-is, keeps the thread title and the human
// text as context — and hands over neither a thread_id nor the retired
// posting-recipe wording (#132: self-posting would double-post).
func TestTalkDispatchContentFraming(t *testing.T) {
	got := talkDispatchContent("引越しの相談", "本文です")
	for _, want := range []string{"引越しの相談", "本文です", "そのまま利用者への返信"} {
		if !strings.Contains(got, want) {
			t.Fatalf("framing missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"thread_id", "レシピ"} {
		if strings.Contains(got, banned) {
			t.Fatalf("framing still contains %q:\n%s", banned, got)
		}
	}
}

// threadMessages lists a thread's stored messages over the public API
// (the same read the /talk UI uses).
func threadMessages(t *testing.T, base, tok, threadID string) []map[string]any {
	t.Helper()
	code, out := doJSONMap(t, "GET", base+"/v1/librarian/threads/"+threadID+"/messages?limit=50", tok, nil)
	if code != http.StatusOK {
		t.Fatalf("list messages: %d %v", code, out)
	}
	raw, _ := out["messages"].([]any)
	msgs := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			msgs = append(msgs, mm)
		}
	}
	return msgs
}

// doJSONMap posts JSON and decodes the response object. Local variant of
// the doJSON helper that returns a map for terse assertions.
func doJSONMap(t *testing.T, method, url, tok string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
