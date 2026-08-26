package runtime

// G3b behaviour pins: effect dispatch, payload rejection, activity
// translation, SSE inbound, discovery, and cursor replay — each against
// the real httpKB client (httptest server) and the real protocol 2
// Conn (net.Pipe fake core).

import (
	"encoding/json"
	"net/http"
	"testing"
)

// startBoundInstance boots a runtime serving testLibA, walks the
// handshake, and binds testThread.
func startBoundInstance(t *testing.T, kb *fakeKBServer, cursors CursorStore) (*harness, *fakeCore) {
	t.Helper()
	kb.setRoster(testLibA())
	h := startRuntime(t, kb, cursors)
	core := h.nextCore()
	core.serveHandshake(testInstanceA, 7)
	core.bind("b-req-1", testBinding1, testThread)
	return h, core
}

// Effect say → POST /v1/librarian/chat → {delivered:true, origin:<id>}.
func TestEffectSayDelivered(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.nextMsgID = "m-stored-42"
	_, core := startBoundInstance(t, kb, nil)

	// Unknown payload members must be tolerated (V2.1 ruling).
	core.send(map[string]any{
		"id": testEffectID, "m": "effect", "binding_id": testBinding1,
		"address": testThread, "kind": "say",
		"payload": map[string]any{"text": "the reply", "future_member": true},
	})
	m := core.recv()
	if got := core.str(m, "id"); got != testEffectID {
		t.Fatalf("effect response id=%q, want %q", got, testEffectID)
	}
	var ok struct {
		Delivered bool   `json:"delivered"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(m["ok"], &ok); err != nil {
		t.Fatalf("effect ok: %v (%v)", err, m)
	}
	if !ok.Delivered || ok.Origin != "m-stored-42" {
		t.Fatalf("effect ok = %+v, want delivered with origin m-stored-42", ok)
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

// Empty / missing text → err response (invalid_field), connection kept.
func TestEffectSayEmptyTextRejected(t *testing.T) {
	kb := newFakeKBServer(t)
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"id": testEffectID, "m": "effect", "binding_id": testBinding1,
		"address": testThread, "kind": "say", "payload": map[string]any{},
	})
	m := core.recv()
	if got := core.str(m, "id"); got != testEffectID {
		t.Fatalf("effect response id=%q, want %q", got, testEffectID)
	}
	var we struct {
		Code string  `json:"code"`
		At   *string `json:"at"`
	}
	if err := json.Unmarshal(m["err"], &we); err != nil {
		t.Fatalf("expected err response, got %v", m)
	}
	if we.Code != "invalid_field" || we.At == nil || *we.At != "payload.text" {
		t.Fatalf("err = %+v, want invalid_field at payload.text", we)
	}
	kb.mu.Lock()
	n := len(kb.chatPosts)
	kb.mu.Unlock()
	if n != 0 {
		t.Fatalf("chat posts = %d, want 0 (nothing must reach the kb)", n)
	}

	// Connection kept: a valid effect right after still works.
	kb.nextMsgID = "m-after"
	core.send(map[string]any{
		"id": testEffectID, "m": "effect", "binding_id": testBinding1,
		"address": testThread, "kind": "say", "payload": map[string]any{"text": "ok"},
	})
	m = core.recv()
	if _, isOK := m["ok"]; !isOK {
		t.Fatalf("follow-up effect not delivered: %v", m)
	}
}

// A definite kb 4xx → {delivered:false} (rejected, not fabricated).
func TestEffectSayKBRejection(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.chatStatus = http.StatusNotFound // e.g. foreign thread: fail-closed 404
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"id": testEffectID, "m": "effect", "binding_id": testBinding1,
		"address": testThread, "kind": "say", "payload": map[string]any{"text": "hi"},
	})
	m := core.recv()
	var ok struct {
		Delivered bool `json:"delivered"`
	}
	if err := json.Unmarshal(m["ok"], &ok); err != nil {
		t.Fatalf("expected ok response, got %v", m)
	}
	if ok.Delivered {
		t.Fatal("4xx must map to delivered:false")
	}
}

// Activity started/progress → chat.status text; ended → done:true.
func TestActivityTranslation(t *testing.T) {
	kb := newFakeKBServer(t)
	_, core := startBoundInstance(t, kb, nil)

	core.send(map[string]any{
		"m": "activity", "address": testThread, "activity_id": "act-1",
		"state": "started", "kind": "turn", "label": "considering…",
	})
	core.send(map[string]any{
		"m": "activity", "address": testThread, "activity_id": "act-1",
		"state": "progress", "label": "searching the stacks…",
	})
	core.send(map[string]any{
		"m": "activity", "address": testThread, "activity_id": "act-1",
		"state": "ended",
	})

	waitFor(t, "three broadcasts", func() bool {
		kb.mu.Lock()
		defer kb.mu.Unlock()
		return len(kb.broadcasts) == 3
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
	if bs[0].Data["text"] != "considering…" || bs[1].Data["text"] != "searching the stacks…" {
		t.Fatalf("progress texts = %v / %v", bs[0].Data["text"], bs[1].Data["text"])
	}
	if done, _ := bs[2].Data["done"].(bool); !done {
		t.Fatalf("ended broadcast = %+v, want done:true", bs[2])
	}
	if _, hasText := bs[2].Data["text"]; hasText {
		t.Fatalf("ended broadcast must not carry text: %+v", bs[2])
	}
}

// SSE human chat.message → SendEvent said with origin = message id.
func TestSSEInboundBecomesSaidEvent(t *testing.T) {
	kb := newFakeKBServer(t)
	cursors := newMemCursors(nil)
	_, core := startBoundInstance(t, kb, cursors)

	kb.pushChatMessage(chatMessageEvent{
		ID: "m-human-7", ThreadID: testThread,
		AuthorUserID: testLibA().UserID, AuthorRole: "human",
		Content: "hello librarian", ThreadIntent: "talk",
		ThreadCreatedBy: testLibA().UserID,
	})
	m := core.recvEvent()
	if got := core.str(m, "kind"); got != "said" {
		t.Fatalf("event kind=%q, want said", got)
	}
	if got := core.str(m, "origin"); got != "m-human-7" {
		t.Fatalf("event origin=%q, want the message id", got)
	}
	if got := core.str(m, "address"); got != testThread {
		t.Fatalf("event address=%q, want %q", got, testThread)
	}
	if got := core.str(m, "binding_id"); got != testBinding1 {
		t.Fatalf("event binding_id=%q, want %q", got, testBinding1)
	}
	if got := contentText(t, m); got != "hello librarian" {
		t.Fatalf("event text=%q", got)
	}
	seq := int64(1)
	core.ok(core.str(m, "id"), map[string]any{"seq": seq, "binding_id": testBinding1})

	waitFor(t, "cursor advance", func() bool {
		return len(cursors.advancedList()) == 1
	})
	if got := cursors.advancedList()[0]; got != testThread+"=m-human-7" {
		t.Fatalf("cursor advanced to %q", got)
	}
}

// Non-human and non-talk messages never become events.
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
	m := core.recvEvent()
	if got := core.str(m, "origin"); got != "m-human-8" {
		t.Fatalf("first event origin=%q, want m-human-8 (filtered frames leaked)", got)
	}
	core.ok(core.str(m, "id"), map[string]any{"seq": int64(1), "binding_id": testBinding1})
}

// Discovery: a librarian appearing on a later poll gets connected.
func TestDiscoveryConnectsNewLibrarian(t *testing.T) {
	kb := newFakeKBServer(t)
	kb.setRoster(testLibA())
	h := startRuntime(t, kb, nil)

	coreA := h.nextCore()
	coreA.serveHandshake(testInstanceA, 1)

	// A librarian with no gate instance yet must NOT be dialed; then it
	// gets provisioned and must be.
	pending := testLibB()
	pending.GateInstanceID = ""
	kb.setRoster(testLibA(), pending)
	kb.setRoster(testLibA(), testLibB())

	coreB := h.nextCore()
	coreB.serveHandshake(testInstanceB, 1)
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
	m := core.recvEvent()
	if got := core.str(m, "origin"); got != "m-3" {
		t.Fatalf("replayed origin=%q, want m-3", got)
	}
	if got := contentText(t, m); got != "missed while down" {
		t.Fatalf("replayed text=%q", got)
	}
	core.ok(core.str(m, "id"), map[string]any{"seq": int64(9), "binding_id": testBinding1})

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

	m := core.recvEvent()
	if got := core.str(m, "origin"); got != "m-1" {
		t.Fatalf("replayed origin=%q, want m-1", got)
	}
	core.ok(core.str(m, "id"), map[string]any{"seq": int64(1), "binding_id": testBinding1})

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

	m := core.recvEvent()
	if got := core.str(m, "origin"); got != "m-2" {
		t.Fatalf("replayed origin=%q, want m-2 (resumed after the HTTP cursor m-1)", got)
	}
	core.ok(core.str(m, "id"), map[string]any{"seq": int64(9), "binding_id": testBinding1})

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
