package api

import (
	"bytes"
	"context"
	"encoding/json"
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
// replies never bounce back into either pipe.
func TestTalkRoutingPersonalLibrarian(t *testing.T) {
	// Mock runtime: capture path + body of every messages call.
	var crabCalls atomic.Int32
	var crabPath, crabBody atomic.Value
	crab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		crabPath.Store(r.URL.Path)
		crabBody.Store(string(b))
		crabCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"s","responses":[{"agent_id":"a","content":"ok"}]}`))
	}))
	defer crab.Close()

	// The REAL client — the request shape the runtime sees is part of
	// the contract under test.
	oc := opencrab.New(crab.URL, "owner-1", "http://kb.test")
	base, adminTok, st := testServerWithTalkDispatch(t, oc)
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
	if msg.UserID != "owner-1" {
		t.Fatalf("runtime caller id: %q, want owner-1", msg.UserID)
	}
	// The thread id and the human text must both be in the content —
	// the librarian's reply recipe posts back by thread_id.
	if !strings.Contains(msg.Content, tid1) || !strings.Contains(msg.Content, "こんにちは司書さん") {
		t.Fatalf("dispatch content missing thread id or body: %q", msg.Content)
	}
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
// cutover guard tests): the REST dispatch leg as a pure recorder.
type fakeTalkDispatcher struct {
	mu    sync.Mutex
	calls []string // agent ids, in dispatch order
}

func (f *fakeTalkDispatcher) DispatchTalk(_ context.Context, agentID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentID)
	return nil
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
