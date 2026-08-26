package runtime

// Behaviour pins: say dispatch (delivered / external_rejected /
// unknown), activity translation, SSE inbound, discovery, and cursor
// replay — each against the real httpKB client (httptest server) and
// the real V3 Conn (net.Pipe fake core).

import (
	"net/http"
	"testing"
	"time"
)

// startBoundInstance boots a runtime serving testLibA, serves the V3
// hello, and binds testThread.
func startBoundInstance(t *testing.T, kb *fakeKBServer, cursors CursorStore) (*harness, *fakeCore) {
	t.Helper()
	kb.setRoster(testLibA())
	h := startRuntime(t, kb, cursors)
	core := h.nextCore()
	core.serveHandshake(testInstanceA)
	core.bind("bind:"+testBinding1, testBinding1, testThread)
	return h, core
}

// Say → POST /v1/librarian/chat → plain {id, m:"ok"} — the stored
// message id does NOT travel back on the wire (V3 §3.3).
func TestSayDelivered(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.nextMsgID = "m-stored-42"
	_, core := startBoundInstance(t, kb, nil)

	// Unknown payload members must be ignored (§3.4).
	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "binding_id": testBinding1,
		"payload": map[string]any{"text": "the reply", "future_member": true},
	})
	m := core.recv()
	if got := core.str(m, "id"); got != testDeliveryID {
		t.Fatalf("say response id=%q, want %q", got, testDeliveryID)
	}
	if got := core.str(m, "m"); got != "ok" {
		t.Fatalf("say response m=%q, want ok (%v)", got, m)
	}
	if _, hasOrigin := m["origin"]; hasOrigin {
		t.Fatalf("say ok carries origin: %v (V3 returns no origin)", m)
	}

	kb.mu.Lock()
	posts := append([]chatPostBody(nil), kb.chatPosts...)
	kb.mu.Unlock()
	if len(posts) != 1 {
		t.Fatalf("chat posts = %d, want 1", len(posts))
	}
	want := chatPostBody{
		ThreadID: testThread, AuthorRole: "assistant",
		AuthorUserID: testLibA().UserID, Intent: "observation", Content: "the reply",
	}
	if posts[0] != want {
		t.Fatalf("chat post = %+v, want %+v", posts[0], want)
	}
	kb.assertBearer(t)
}

// Empty / missing text → err(code="external_rejected"), zero kb I/O,
// connection kept (§3.4).
func TestSayEmptyTextExternallyRejected(t *testing.T) {
	kb := newFakeKBServer(t)
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "binding_id": testBinding1,
		"payload": map[string]any{},
	})
	m := core.recv()
	if got := core.str(m, "id"); got != testDeliveryID {
		t.Fatalf("say response id=%q, want %q", got, testDeliveryID)
	}
	if core.str(m, "m") != "err" || core.str(m, "code") != "external_rejected" {
		t.Fatalf("say response = %v, want err external_rejected", m)
	}
	kb.mu.Lock()
	n := len(kb.chatPosts)
	kb.mu.Unlock()
	if n != 0 {
		t.Fatalf("chat posts = %d, want 0 (nothing must reach the kb)", n)
	}

	// Connection kept: a valid say right after still works.
	kb.nextMsgID = "m-after"
	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "binding_id": testBinding1,
		"payload": map[string]any{"text": "ok"},
	})
	m = core.recv()
	if core.str(m, "m") != "ok" {
		t.Fatalf("follow-up say not delivered: %v", m)
	}
}

// A definite kb 4xx → err(code="external_rejected") — the only say
// failure code; nothing is fabricated.
func TestSayKBRejection(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.chatStatus = http.StatusNotFound // e.g. foreign thread: fail-closed 404
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "binding_id": testBinding1,
		"payload": map[string]any{"text": "hi"},
	})
	m := core.recv()
	if core.str(m, "m") != "err" || core.str(m, "code") != "external_rejected" {
		t.Fatalf("say response = %v, want err external_rejected", m)
	}
}

// A kb 5xx is indeterminate: no ok, no err — the socket closes without
// answering (no fabrication; the core records the delivery
// indeterminate).
func TestSayKBServerErrorClosesWithoutAnswering(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.chatStatus = http.StatusInternalServerError
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "binding_id": testBinding1,
		"payload": map[string]any{"text": "hi"},
	})
	core.expectClosed()
}

// Activity started → chat.status generic text; ended → done:true. V3
// activity carries no label and no progress state.
func TestActivityTranslation(t *testing.T) {
	kb := newFakeKBServer(t)
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"m": "activity", "binding_id": testBinding1, "activity_id": "act-1",
		"state": "started",
	})
	core.send(map[string]any{
		"m": "activity", "binding_id": testBinding1, "activity_id": "act-1",
		"state": "ended",
	})

	waitFor(t, "two broadcasts", func() bool {
		kb.mu.Lock()
		defer kb.mu.Unlock()
		return len(kb.broadcasts) == 2
	})
	kb.mu.Lock()
	bs := append([]broadcastBody(nil), kb.broadcasts...)
	kb.mu.Unlock()
	for i, b := range bs {
		if b.Type != "chat.status" || b.Data["thread_id"] != testThread ||
			b.AuthorUserID != testLibA().UserID {
			t.Fatalf("broadcast[%d] = %+v", i, b)
		}
	}
	if bs[0].Data["text"] != activityStartedStatus {
		t.Fatalf("started text = %v, want %q", bs[0].Data["text"], activityStartedStatus)
	}
	if done, _ := bs[1].Data["done"].(bool); !done {
		t.Fatalf("ended broadcast = %+v, want done:true", bs[1])
	}
	if _, hasText := bs[1].Data["text"]; hasText {
		t.Fatalf("ended broadcast must not carry text: %+v", bs[1])
	}
}

// SSE human chat.message → SendSaid with origin = message id.
func TestSSEInboundBecomesSaid(t *testing.T) {
	kb := newFakeKBServer(t)
	cursors := newMemCursors(nil)
	_, core := startBoundInstance(t, kb, cursors)

	kb.pushChatMessage(chatMessageEvent{
		ID: "m-human-7", ThreadID: testThread,
		AuthorUserID: testLibA().UserID, AuthorRole: "human",
		Content: "hello librarian", ThreadIntent: "talk",
		ThreadCreatedBy: testLibA().UserID,
	})
	m := core.recvSaid()
	if got := core.str(m, "origin"); got != "m-human-7" {
		t.Fatalf("said origin=%q, want the message id", got)
	}
	if got := core.str(m, "binding_id"); got != testBinding1 {
		t.Fatalf("said binding_id=%q, want %q", got, testBinding1)
	}
	if got := core.str(m, "author_id"); got != testLibA().UserID {
		t.Fatalf("said author_id=%q", got)
	}
	if got := saidText(t, m); got != "hello librarian" {
		t.Fatalf("said text=%q", got)
	}
	if got := string(m["attachments"]); got != "[]" {
		t.Fatalf("said attachments=%s, want []", got)
	}
	core.okSeq(core.str(m, "id"), 1)

	waitFor(t, "cursor advance", func() bool {
		return len(cursors.advancedList()) == 1
	})
	if got := cursors.advancedList()[0]; got != testThread+"=m-human-7" {
		t.Fatalf("cursor advanced to %q", got)
	}
}

// Non-human and non-talk messages never become saids.
func TestSSEInboundFiltersNonHuman(t *testing.T) {
	kb := newFakeKBServer(t)
	_, core := startBoundInstance(t, kb, nil)

	kb.pushChatMessage(chatMessageEvent{
		ID: "m-assistant-1", ThreadID: testThread,
		AuthorUserID: testLibA().UserID, AuthorRole: "assistant",
		Content: "my own reply echoing back", ThreadIntent: "talk",
		ThreadCreatedBy: testLibA().UserID,
	})
	kb.pushChatMessage(chatMessageEvent{
		ID: "m-coord-1", ThreadID: "thread-0000bbbb",
		AuthorUserID: "u-somebody", AuthorRole: "human",
		Content: "coordination room chatter", ThreadIntent: "",
		ThreadCreatedBy: "u-somebody",
	})
	// A message that DOES qualify, to prove the stream advanced past
	// the filtered ones.
	kb.pushChatMessage(chatMessageEvent{
		ID: "m-human-8", ThreadID: testThread,
		AuthorUserID: testLibA().UserID, AuthorRole: "human",
		Content: "real question", ThreadIntent: "talk",
		ThreadCreatedBy: testLibA().UserID,
	})
	m := core.recvSaid()
	if got := core.str(m, "origin"); got != "m-human-8" {
		t.Fatalf("first said origin=%q, want m-human-8 (filtered frames leaked)", got)
	}
	core.okSeq(core.str(m, "id"), 1)
}

// Discovery: a librarian appearing on a later poll gets connected.
func TestDiscoveryConnectsNewLibrarian(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.setRoster(testLibA())
	h := startRuntime(t, kb, nil)

	coreA := h.nextCore()
	coreA.serveHandshake(testInstanceA)

	// A librarian with no gate instance yet must NOT be dialed; then it
	// gets provisioned and must be.
	pending := testLibB()
	pending.GateInstanceID = ""
	kb.setRoster(testLibA(), pending)
	kb.setRoster(testLibA(), testLibB())

	coreB := h.nextCore()
	coreB.serveHandshake(testInstanceB)
}

// Discovery, the reverse direction: an instance that leaves the roster
// gets its connection closed and its reconnect loop stopped — the
// platform answers revision POST / instance DELETE with 409
// instance_active while a live connection exists (V3 §5.5), so the
// gate must actually let go. Reappearing later is a normal
// new-instance connect.
func TestDiscoveryDisconnectsRemovedLibrarian(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.setRoster(testLibA(), testLibB())
	h := startRuntime(t, kb, nil)

	// Both instances connect; dial order is map order, so identify the
	// connections by their hello.
	cores := map[string]*fakeCore{}
	for i := 0; i < 2; i++ {
		c := h.nextCore()
		cores[c.serveHandshakeAny()] = c
	}
	coreA, coreB := cores[testInstanceA], cores[testInstanceB]
	if coreA == nil || coreB == nil {
		t.Fatalf("connected instances = %v, want A and B", cores)
	}

	// B leaves the roster → the next poll closes B's connection.
	kb.setRoster(testLibA())
	coreB.expectClosed()

	// …and B's runner is gone for good: across several discovery and
	// backoff cycles no reconnect is dialed (A stays connected and never
	// redials, so any arrival here would be B).
	time.Sleep(8 * 25 * time.Millisecond)
	select {
	case <-h.cores:
		t.Fatal("removed instance was redialed")
	default:
	}

	// A is unaffected: its live connection still serves a bind.
	coreA.bind("bind:"+testBinding1, testBinding1, testThread)

	// B reappearing on a later poll reconnects like any new librarian.
	kb.setRoster(testLibA(), testLibB())
	coreB2 := h.nextCore()
	coreB2.serveHandshake(testInstanceB)
}

// Reconnect: a new session replays human messages after the stored
// cursor, with origin idempotency, and advances the cursor.
func TestReconnectReplaysFromCursor(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.messages[testThread] = []ChatMessage{
		{ID: "m-1", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "already sent"},
		{ID: "m-2", AuthorRole: "assistant", AuthorUserID: testLibA().UserID, Content: "old reply"},
		{ID: "m-3", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "missed while down"},
	}
	cursors := newMemCursors(map[string]string{testThread: "m-1"})
	_, core := startBoundInstance(t, kb, cursors)

	// The bind triggers the replay: m-2 is skipped (not human), m-3 is
	// re-sent with origin m-3.
	m := core.recvSaid()
	if got := core.str(m, "origin"); got != "m-3" {
		t.Fatalf("replayed origin=%q, want m-3", got)
	}
	if got := saidText(t, m); got != "missed while down" {
		t.Fatalf("replayed text=%q", got)
	}
	core.okSeq(core.str(m, "id"), 9)

	waitFor(t, "cursor advance to m-3", func() bool {
		list := cursors.advancedList()
		return len(list) == 1 && list[0] == testThread+"=m-3"
	})
	kb.mu.Lock()
	since := append([]string(nil), kb.sinceSeen...)
	kb.mu.Unlock()
	if len(since) == 0 || since[0] != "m-1" {
		t.Fatalf("replay listed since=%v, want first query from the cursor m-1", since)
	}
}

// The production default cursor store is the httpKB itself (nil Cursors
// + real KB): with no binding row the GET cursor 404s, so replay runs
// from the beginning, and the replay read is stamped with the thread
// owner (?author_user_id=) so the user-less gateway token clears the
// ownership check. Never fails the runner.
func TestReplayReadsCursorOverHTTPNoBinding(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.messages[testThread] = []ChatMessage{
		{ID: "m-1", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "first"},
	}
	_, core := startBoundInstance(t, kb, nil) // nil → httpKB CursorStore

	m := core.recvSaid()
	if got := core.str(m, "origin"); got != "m-1" {
		t.Fatalf("replayed origin=%q, want m-1", got)
	}
	core.okSeq(core.str(m, "id"), 1)

	kb.mu.Lock()
	since := append([]string(nil), kb.sinceSeen...)
	kb.mu.Unlock()
	if len(since) == 0 || since[0] != "" {
		t.Fatalf("since=%v, want replay from the beginning (no binding → GET cursor 404)", since)
	}
	if authors := kb.authorList(); len(authors) == 0 || authors[0] != testLibA().UserID {
		t.Fatalf("replay author stamp=%v, want owner %q on the first read", authors, testLibA().UserID)
	}
}

// With a binding row, the default httpKB cursor store reads the stored
// cursor over HTTP (GET) and advances it over HTTP (PUT) after the
// confirmed send — no in-memory stub involved.
func TestReplayReadsAndAdvancesCursorOverHTTP(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.messages[testThread] = []ChatMessage{
		{ID: "m-1", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "already sent"},
		{ID: "m-2", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "missed while down"},
	}
	// Seed a binding row with cursor at m-1: GET must return it so replay
	// resumes after m-1 (m-2 only).
	kb.cursors[testThread] = "m-1"
	_, core := startBoundInstance(t, kb, nil) // nil → httpKB CursorStore

	m := core.recvSaid()
	if got := core.str(m, "origin"); got != "m-2" {
		t.Fatalf("replayed origin=%q, want m-2 (resumed after the HTTP cursor m-1)", got)
	}
	core.okSeq(core.str(m, "id"), 9)

	waitFor(t, "cursor advance PUT to m-2", func() bool {
		puts := kb.cursorPutList()
		return len(puts) == 1 && puts[0] == testThread+"=m-2"
	})
	kb.mu.Lock()
	since := append([]string(nil), kb.sinceSeen...)
	kb.mu.Unlock()
	if len(since) == 0 || since[0] != "m-1" {
		t.Fatalf("replay listed since=%v, want first query from the HTTP cursor m-1", since)
	}
	if authors := kb.authorList(); len(authors) == 0 || authors[0] != testLibA().UserID {
		t.Fatalf("replay author stamp=%v, want owner %q", authors, testLibA().UserID)
	}
}

// A replayed said the core answers with seq null (discarded) does not
// stop the replay; the cursor still advances.
func TestReplayContinuesPastDiscardedSaid(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.messages[testThread] = []ChatMessage{
		{ID: "m-1", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "first"},
		{ID: "m-2", AuthorRole: "human", AuthorUserID: testLibA().UserID, Content: "second"},
	}
	cursors := newMemCursors(nil)
	_, core := startBoundInstance(t, kb, cursors)

	m := core.recvSaid()
	core.okSeq(core.str(m, "id"), nil) // core did not record m-1
	m = core.recvSaid()
	if got := core.str(m, "origin"); got != "m-2" {
		t.Fatalf("second replayed origin=%q, want m-2", got)
	}
	core.okSeq(core.str(m, "id"), 2)
	waitFor(t, "both cursor advances", func() bool {
		return len(cursors.advancedList()) == 2
	})
}

// Config refuses to start on missing socket or token.
func TestConfigRefusesIncomplete(t *testing.T) {
	cfg := Config{
		SocketPath: "", KBBaseURL: "http://kb", Token: "",
		DiscoveryInterval: 1, ReconnectMin: 1, ReconnectMax: 1, HelloRevision: 1,
	}
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("New accepted a config without socket and token")
	}
}
