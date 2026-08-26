package runtime

// Test harness: a scripted fake gate core on the far side of net.Pipe
// (the Dial seam) and an httptest omoikane server behind the real
// httpKB client. All identifiers are synthetic.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testInstanceA = "0190a1b2-c3d4-7e5f-8a6b-000000000001"
	testInstanceB = "0190a1b2-c3d4-7e5f-8a6b-000000000002"
	testBinding1  = "0190a1b2-c3d4-7e5f-8a6b-0000000000aa"
	testEffectID  = "0190a1b2-c3d4-7e5f-8a6b-0000000000ee"
	testThread    = "thread-0000aaaa"
	testToken     = "tok-gateway-test"
)

func testLibA() Librarian {
	return Librarian{UserID: "u-owner-a", AgentID: "plib-u-owner-a", Name: "A", GateInstanceID: testInstanceA}
}

func testLibB() Librarian {
	return Librarian{UserID: "u-owner-b", AgentID: "plib-u-owner-b", Name: "B", GateInstanceID: testInstanceB}
}

// ---- fake omoikane server -------------------------------------------

// chatPostBody mirrors what the gate is expected to POST.
type chatPostBody struct {
	ThreadID     string `json:"thread_id"`
	AuthorRole   string `json:"author_role"`
	AuthorUserID string `json:"author_user_id"`
	Intent       string `json:"intent"`
	Content      string `json:"content"`
}

type broadcastBody struct {
	Type         string         `json:"type"`
	Data         map[string]any `json:"data"`
	AuthorUserID string         `json:"author_user_id"`
}

// fakeKBServer is the httptest omoikane. Mutable state is mutex-held so
// tests can reshape the roster or the reply status mid-flight.
type fakeKBServer struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	roster      []Librarian
	chatStatus  int    // response status for POST /v1/librarian/chat
	nextMsgID   string // id answered on 201
	chatPosts   []chatPostBody
	broadcasts  []broadcastBody
	messages    map[string][]ChatMessage // thread → full history (oldest first)
	sinceSeen   []string                 // captured ?since= values
	authorSeen  []string                 // captured ?author_user_id= values
	cursors     map[string]string        // thread → last_sent_message_id (key present = binding row exists)
	cursorPuts  []string                 // "thread=msg" advance calls in order
	sse         chan string              // preformatted SSE blocks
	lastAuth    string
	sseConnects int
}

func newFakeKBServer(t *testing.T) *fakeKBServer {
	f := &fakeKBServer{
		t:          t,
		chatStatus: http.StatusCreated,
		nextMsgID:  "m-reply-1",
		messages:   map[string][]ChatMessage{},
		cursors:    map[string]string{},
		sse:        make(chan string, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gateway/librarians", f.handleRoster)
	mux.HandleFunc("POST /v1/librarian/chat", f.handleChat)
	mux.HandleFunc("POST /v1/events/broadcast", f.handleBroadcast)
	mux.HandleFunc("GET /v1/librarian/threads/{id}/messages", f.handleMessages)
	mux.HandleFunc("GET /v1/gateway/threads/{id}/cursor", f.handleGetCursor)
	mux.HandleFunc("PUT /v1/gateway/threads/{id}/cursor", f.handlePutCursor)
	mux.HandleFunc("GET /v1/events", f.handleSSE)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKBServer) auth(r *http.Request) {
	f.mu.Lock()
	f.lastAuth = r.Header.Get("Authorization")
	f.mu.Unlock()
}

func (f *fakeKBServer) handleRoster(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	f.mu.Lock()
	roster := append([]Librarian(nil), f.roster...)
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"librarians": roster})
}

func (f *fakeKBServer) handleChat(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	var body chatPostBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("chat post decode: %v", err)
	}
	f.mu.Lock()
	f.chatPosts = append(f.chatPosts, body)
	status, id := f.chatStatus, f.nextMsgID
	f.mu.Unlock()
	if status != http.StatusCreated {
		writeJSON(w, status, map[string]any{"error": "scripted failure"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (f *fakeKBServer) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	var body broadcastBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("broadcast decode: %v", err)
	}
	f.mu.Lock()
	f.broadcasts = append(f.broadcasts, body)
	f.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"published": true})
}

func (f *fakeKBServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	thread := r.PathValue("id")
	since := r.URL.Query().Get("since")
	f.mu.Lock()
	f.sinceSeen = append(f.sinceSeen, since)
	f.authorSeen = append(f.authorSeen, r.URL.Query().Get("author_user_id"))
	hist := f.messages[thread]
	f.mu.Unlock()
	out := hist
	if since != "" {
		out = nil
		found := false
		for _, m := range hist {
			if found {
				out = append(out, m)
			}
			if m.ID == since {
				found = true
			}
		}
	}
	if out == nil {
		out = []ChatMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

// handleGetCursor serves GET /v1/gateway/threads/{id}/cursor: 404 when
// no binding row (no map key), else the stored last_sent_message_id.
func (f *fakeKBServer) handleGetCursor(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	thread := r.PathValue("id")
	f.mu.Lock()
	cur, ok := f.cursors[thread]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no binding"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread_id": thread, "last_sent_message_id": cur})
}

// handlePutCursor serves PUT /v1/gateway/threads/{id}/cursor: records
// the advance, 404 when no binding row (mirrors the real server, where
// the cursor only trails an existing binding).
func (f *fakeKBServer) handlePutCursor(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	thread := r.PathValue("id")
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("cursor put decode: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.cursors[thread]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no binding"})
		return
	}
	f.cursors[thread] = body.MessageID
	f.cursorPuts = append(f.cursorPuts, thread+"="+body.MessageID)
	writeJSON(w, http.StatusOK, map[string]any{"thread_id": thread, "last_sent_message_id": body.MessageID})
}

func (f *fakeKBServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	f.auth(r)
	f.mu.Lock()
	f.sseConnects++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl := w.(http.Flusher)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case block := <-f.sse:
			fmt.Fprint(w, block)
			fl.Flush()
		}
	}
}

// pushChatMessage feeds one chat.message through the SSE stream.
func (f *fakeKBServer) pushChatMessage(m chatMessageEvent) {
	data, _ := json.Marshal(m)
	f.sse <- "event: chat.message\ndata: " + string(data) + "\n\n"
}

func (f *fakeKBServer) setRoster(libs ...Librarian) {
	f.mu.Lock()
	f.roster = libs
	f.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// waitFor polls cond until true or the deadline trips.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- fake core ------------------------------------------------------

// fakeCore drives the core side of one net.Pipe.
type fakeCore struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func newFakeCore(t *testing.T, c net.Conn) *fakeCore {
	t.Helper()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &fakeCore{t: t, c: c, br: bufio.NewReader(c)}
}

// recv reads one LF frame as a generic map.
func (f *fakeCore) recv() map[string]json.RawMessage {
	f.t.Helper()
	line, err := f.br.ReadBytes('\n')
	if err != nil {
		f.t.Fatalf("fake core read: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
		f.t.Fatalf("fake core unmarshal %q: %v", line, err)
	}
	return m
}

func (f *fakeCore) str(m map[string]json.RawMessage, key string) string {
	f.t.Helper()
	var s string
	if err := json.Unmarshal(m[key], &s); err != nil {
		f.t.Fatalf("fake core member %s in %v: %v", key, m, err)
	}
	return s
}

func (f *fakeCore) send(v any) {
	f.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatalf("fake core marshal: %v", err)
	}
	if _, err := f.c.Write(append(b, '\n')); err != nil {
		f.t.Fatalf("fake core write: %v", err)
	}
}

func (f *fakeCore) ok(id string, payload any) {
	f.send(map[string]any{"id": id, "ok": payload})
}

// serveHandshake serves hello (epoch) and ready, asserting the hello's
// instance_id, and returns after the gate is ACTIVE-ready.
func (f *fakeCore) serveHandshake(wantInstance string, epoch uint64) {
	f.t.Helper()
	m := f.recv()
	if got := f.str(m, "m"); got != "hello" {
		f.t.Fatalf("first frame m=%q, want hello", got)
	}
	if got := f.str(m, "instance_id"); got != wantInstance {
		f.t.Fatalf("hello instance_id=%q, want %q", got, wantInstance)
	}
	f.ok(f.str(m, "id"), map[string]any{"protocol": 2, "connection_epoch": epoch})
	m = f.recv()
	if got := f.str(m, "m"); got != "ready" {
		f.t.Fatalf("frame m=%q, want ready", got)
	}
	f.ok(f.str(m, "id"), map[string]any{})
}

// bind sends a bind for (bindingID, address) and consumes the ack.
func (f *fakeCore) bind(reqID, bindingID, address string) {
	f.t.Helper()
	f.send(map[string]any{"id": reqID, "m": "bind", "binding_id": bindingID, "address": address})
	m := f.recv()
	if got := f.str(m, "id"); got != reqID {
		f.t.Fatalf("bind ack id=%q, want %q", got, reqID)
	}
	if _, isOK := m["ok"]; !isOK {
		f.t.Fatalf("bind answered err: %v", m)
	}
}

// recvEvent reads frames until an event request arrives (skipping
// nothing — the gate only sends events after the handshake here) and
// returns it decoded.
func (f *fakeCore) recvEvent() map[string]json.RawMessage {
	f.t.Helper()
	m := f.recv()
	if got := f.str(m, "m"); got != "event" {
		f.t.Fatalf("frame m=%q, want event", got)
	}
	return m
}

// ---- harness --------------------------------------------------------

type harness struct {
	t     *testing.T
	kb    *fakeKBServer
	cores chan net.Conn
	rt    *Runtime
	stop  context.CancelFunc
	ended chan struct{}
}

// startRuntime builds and runs a Runtime against the fake KB server and
// a piped Dial. cursors nil = the production noCursorStore.
func startRuntime(t *testing.T, kb *fakeKBServer, cursors CursorStore) *harness {
	t.Helper()
	cores := make(chan net.Conn, 8)
	cfg := Config{
		SocketPath:        "/nonexistent/test.sock",
		KBBaseURL:         kb.srv.URL,
		Token:             testToken,
		DiscoveryInterval: 25 * time.Millisecond,
		ReconnectMin:      10 * time.Millisecond,
		ReconnectMax:      50 * time.Millisecond,
		HelloRevision:     1,
		Cursors:           cursors,
		Dial: func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
			client, server := net.Pipe()
			cores <- server
			return client, nil
		},
	}
	rt, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_ = rt.Run(ctx)
	}()
	h := &harness{t: t, kb: kb, cores: cores, rt: rt, stop: cancel, ended: ended}
	t.Cleanup(func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not stop")
		}
	})
	return h
}

// nextCore waits for the next dialed connection.
func (h *harness) nextCore() *fakeCore {
	h.t.Helper()
	select {
	case c := <-h.cores:
		return newFakeCore(h.t, c)
	case <-time.After(5 * time.Second):
		h.t.Fatal("no core connection arrived")
		return nil
	}
}

// memCursors is an in-memory CursorStore for replay tests.
type memCursors struct {
	mu       sync.Mutex
	cursors  map[string]string
	advanced []string // "thread=msg" in call order
}

func newMemCursors(seed map[string]string) *memCursors {
	if seed == nil {
		seed = map[string]string{}
	}
	return &memCursors{cursors: seed}
}

func (m *memCursors) Cursor(_ context.Context, threadID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[threadID], nil
}

func (m *memCursors) Advance(_ context.Context, threadID, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[threadID] = messageID
	m.advanced = append(m.advanced, threadID+"="+messageID)
	return nil
}

func (m *memCursors) advancedList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.advanced...)
}

// contentText extracts content.text from an event frame.
func contentText(t *testing.T, m map[string]json.RawMessage) string {
	t.Helper()
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m["content"], &c); err != nil {
		t.Fatalf("event content: %v", err)
	}
	return c.Text
}

// authorList returns the captured ?author_user_id= values, in order.
func (f *fakeKBServer) authorList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authorSeen...)
}

// cursorPutList returns the "thread=msg" cursor advances, in order.
func (f *fakeKBServer) cursorPutList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cursorPuts...)
}

// hasPrefixAuth asserts the recorded Authorization header carried the
// gateway token.
func (f *fakeKBServer) assertBearer(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(f.lastAuth, "Bearer ") || !strings.Contains(f.lastAuth, testToken) {
		t.Fatalf("Authorization = %q, want Bearer %s", f.lastAuth, testToken)
	}
}
