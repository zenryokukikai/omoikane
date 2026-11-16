package api

// Gateway scope (issue #104 G3a, C案): author attribution on chat +
// broadcast, the /v1/gateway/librarians roster, the per-instance
// routed-thread list, SSE-equivalent visibility, and the
// thread-creation routing hook (protocol 3: the REAL
// opencrab.GateProvisioner, which makes no core call).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/config"
	"github.com/zenryokukikai/omoikane/internal/enrich"
	"github.com/zenryokukikai/omoikane/internal/gate"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// gwSetup is testServer plus two ordinary users, the gateway service
// token (user-less, read+write+gateway) and alice's user token.
func gwSetup(t *testing.T) (base string, st *store.Store, gwTok, aliceTok, adminTok string) {
	t.Helper()
	base, adminTok, st = testServer(t)
	ctx := context.Background()
	for _, u := range []string{"alice", "bob"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	gwTok, err = st.CreateToken(ctx, "", "gateway-svc", []string{"read", "write", "gateway"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	aliceTok, err = st.CreateToken(ctx, "alice", "alice-tok", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func gwOpenTalkThread(t *testing.T, st *store.Store, owner string) string {
	t.Helper()
	id, err := st.OpenThread(context.Background(), &store.ChatThread{
		Title: owner + " talk", Intent: "talk", CreatedBy: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Gateway scope + stamped author: the message is attributed to the
// stamped user and the thread check runs as that user.
func TestGatewayChatPostStampsAuthor(t *testing.T) {
	base, st, gwTok, _, _ := gwSetup(t)
	th := gwOpenTalkThread(t, st, "alice")

	s, raw := doJSON(t, http.MethodPost, base+"/v1/librarian/chat", gwTok, map[string]any{
		"thread_id": th, "author_role": "human", "content": "relayed hello",
		"author_user_id": "alice",
	}, nil)
	if s != http.StatusCreated {
		t.Fatalf("gateway chat post: %d %s", s, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	msg, err := st.GetChatMessage(context.Background(), out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.AuthorUserID != "alice" {
		t.Errorf("author_user_id = %q, want alice (the stamped owner)", msg.AuthorUserID)
	}
}

// The stamped user's visibility gates the thread: a talk thread the
// stamped user does not own is one indistinguishable 404 — and so is a
// gateway call with no stamp at all (user-less token).
func TestGatewayChatPostForeignThread404(t *testing.T) {
	base, st, gwTok, _, _ := gwSetup(t)
	bobTh := gwOpenTalkThread(t, st, "bob")

	s, raw := doJSON(t, http.MethodPost, base+"/v1/librarian/chat", gwTok, map[string]any{
		"thread_id": bobTh, "author_role": "human", "content": "x",
		"author_user_id": "alice",
	}, nil)
	if s != http.StatusNotFound {
		t.Fatalf("stamped alice into bob's thread: %d %s, want 404", s, raw)
	}

	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/chat", gwTok, map[string]any{
		"thread_id": bobTh, "author_role": "human", "content": "x",
	}, nil)
	if s != http.StatusNotFound {
		t.Fatalf("gateway without stamp into talk thread: %d %s, want 404", s, raw)
	}
}

// Regression pin: outside the literal "gateway" scope the field is
// ignored exactly as before — for a user token AND for an admin token
// (the admin scope wildcard must not extend to author attribution).
func TestChatPostAuthorFieldIgnoredOutsideGateway(t *testing.T) {
	base, st, _, aliceTok, adminTok := gwSetup(t)
	th := gwOpenTalkThread(t, st, "alice")

	post := func(tok, stamp string) *store.ChatMessage {
		t.Helper()
		s, raw := doJSON(t, http.MethodPost, base+"/v1/librarian/chat", tok, map[string]any{
			"thread_id": th, "author_role": "human", "content": "hi",
			"author_user_id": stamp,
		}, nil)
		if s != http.StatusCreated {
			t.Fatalf("chat post: %d %s", s, raw)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		msg, err := st.GetChatMessage(context.Background(), out.ID)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	if msg := post(aliceTok, "bob"); msg.AuthorUserID != "alice" {
		t.Errorf("user token: author = %q, want alice (field ignored)", msg.AuthorUserID)
	}
	if msg := post(adminTok, "alice"); msg.AuthorUserID != "admin" {
		t.Errorf("admin token: author = %q, want admin (wildcard must not stamp)", msg.AuthorUserID)
	}
}

// Broadcast under the gateway scope: chat.status lands when the stamp
// matches the thread owner, 404 otherwise (same fail-closed shape as
// slice 4), and the field is ignored for ordinary tokens.
func TestGatewayBroadcast(t *testing.T) {
	base, st, gwTok, aliceTok, _ := gwSetup(t)
	aliceTh := gwOpenTalkThread(t, st, "alice")
	bobTh := gwOpenTalkThread(t, st, "bob")

	bc := func(tok, threadID, stamp string) (int, []byte) {
		body := map[string]any{
			"type": "chat.status",
			"data": map[string]any{"thread_id": threadID, "state": "searching"},
		}
		if stamp != "" {
			body["author_user_id"] = stamp
		}
		return doJSON(t, http.MethodPost, base+"/v1/events/broadcast", tok, body, nil)
	}

	if s, raw := bc(gwTok, aliceTh, "alice"); s != http.StatusAccepted {
		t.Errorf("gateway stamped owner: %d %s, want 202", s, raw)
	}
	if s, raw := bc(gwTok, aliceTh, "bob"); s != http.StatusNotFound {
		t.Errorf("gateway stamped non-owner: %d %s, want 404", s, raw)
	}
	if s, raw := bc(gwTok, aliceTh, ""); s != http.StatusNotFound {
		t.Errorf("gateway without stamp: %d %s, want 404", s, raw)
	}
	// Ordinary token: the field is ignored, so stamping bob does not
	// open bob's thread (regression pin).
	if s, raw := bc(aliceTok, bobTh, "bob"); s != http.StatusNotFound {
		t.Errorf("user token stamping bob: %d %s, want 404 (field ignored)", s, raw)
	}
	// And alice still reaches her own thread with the field present.
	if s, raw := bc(aliceTok, aliceTh, "bob"); s != http.StatusAccepted {
		t.Errorf("owner with ignored field: %d %s, want 202", s, raw)
	}
}

// GET /v1/gateway/librarians: happy path + the uniform wrong-scope 403
// (RequireScope's answer, pinned — the leak-guard ledger entry points
// here).
func TestGatewayLibrarians(t *testing.T) {
	base, st, gwTok, aliceTok, adminTok := gwSetup(t)
	ctx := context.Background()

	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "AliceLib",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserLibrarianGateInstance(ctx, "alice", "0192aaaa-bbbb-7ccc-8ddd-eeeeffff0000"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "bob", AgentID: "plib-bob", Name: "BobLib", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}

	s, raw := doJSON(t, http.MethodGet, base+"/v1/gateway/librarians", gwTok, nil, nil)
	if s != http.StatusOK {
		t.Fatalf("gateway token: %d %s", s, raw)
	}
	var out struct {
		Librarians []struct {
			UserID         string `json:"user_id"`
			AgentID        string `json:"agent_id"`
			Name           string `json:"name"`
			GateInstanceID string `json:"gate_instance_id"`
		} `json:"librarians"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Librarians) != 1 {
		t.Fatalf("roster = %s, want exactly alice (bob is disabled)", raw)
	}
	l := out.Librarians[0]
	if l.UserID != "alice" || l.AgentID != "plib-alice" || l.Name != "AliceLib" ||
		l.GateInstanceID != "0192aaaa-bbbb-7ccc-8ddd-eeeeffff0000" {
		t.Errorf("alice row = %+v", l)
	}

	// Wrong scope: RequireScope's uniform 403, no roster bytes.
	s, raw = doJSON(t, http.MethodGet, base+"/v1/gateway/librarians", aliceTok, nil, nil)
	if s != http.StatusForbidden {
		t.Errorf("read/write token: %d, want 403", s)
	}
	if strings.Contains(string(raw), "plib-alice") {
		t.Errorf("403 body leaks roster: %s", raw)
	}
	// No token: 401 from the auth middleware.
	if s, _ := doJSON(t, http.MethodGet, base+"/v1/gateway/librarians", "", nil, nil); s != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", s)
	}
	// Admin wildcard satisfies RequireScope (the one existing wildcard
	// contract) — documented, not weakened.
	if s, _ := doJSON(t, http.MethodGet, base+"/v1/gateway/librarians", adminTok, nil, nil); s != http.StatusOK {
		t.Errorf("admin token: %d, want 200", s)
	}
}

// G3c hardening: a gateway stamp confers EXACTLY the stamped user's
// ownership, never agent authority — even when the gateway token's OWN
// user is an agent-role user (the mis-mint the review flagged). Pre-fix,
// the agent exception (keyed on the caller) would have let the stamp
// reach any thread; now the stamp path never consults the role.
func TestGatewayStampNeverMintsAgentAuthority(t *testing.T) {
	base, st, _, _, _ := gwSetup(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: "agent-bot", Name: "bot", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	agentGwTok, err := st.CreateToken(ctx, "agent-bot", "gw-agent", []string{"read", "write", "gateway"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bobTh := gwOpenTalkThread(t, st, "bob")

	// Stamp alice — a user who does NOT own bob's thread — with an
	// agent-role gateway token. Must be a uniform 404, not a leak.
	s, raw := doJSON(t, http.MethodPost, base+"/v1/librarian/chat", agentGwTok, map[string]any{
		"thread_id": bobTh, "author_role": "human", "content": "impersonation attempt",
		"author_user_id": "alice",
	}, nil)
	if s != http.StatusNotFound {
		t.Fatalf("agent-role gateway token stamping alice into bob's thread: %d %s, want 404", s, raw)
	}

	// Non-regression: the SAME agent-role gateway token stamping the
	// actual owner into the owner's own thread still succeeds — the fix
	// narrows to ownership, it does not break legitimate relays.
	bobOwnTh := gwOpenTalkThread(t, st, "bob")
	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/chat", agentGwTok, map[string]any{
		"thread_id": bobOwnTh, "author_role": "human", "content": "legit relay",
		"author_user_id": "bob",
	}, nil)
	if s != http.StatusCreated {
		t.Fatalf("agent-role gateway token stamping bob into bob's own thread: %d %s, want 201", s, raw)
	}
}

// GET /v1/gateway/instances/{id}/threads + PUT
// /v1/gateway/threads/{id}/cursor: the routed-thread list carries each
// thread's cursor, the advance guards message membership, and both
// answer the uniform wrong-scope 403.
func TestGatewayThreadCursor(t *testing.T) {
	base, st, gwTok, aliceTok, _ := gwSetup(t)
	ctx := context.Background()
	th := gwOpenTalkThread(t, st, "alice")
	otherTh := gwOpenTalkThread(t, st, "alice")
	msgID, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: th, AuthorRole: "human", AuthorUserID: "alice", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	otherMsgID, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: otherTh, AuthorRole: "human", AuthorUserID: "alice", Content: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}

	cursorURL := base + "/v1/gateway/threads/" + th + "/cursor"
	listURL := base + "/v1/gateway/instances/inst-1/threads"
	decodeList := func(raw []byte) map[string]string {
		t.Helper()
		var out struct {
			Threads []struct {
				ThreadID          string `json:"thread_id"`
				LastSentMessageID string `json:"last_sent_message_id"`
			} `json:"threads"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode list %s: %v", raw, err)
		}
		m := map[string]string{}
		for _, r := range out.Threads {
			m[r.ThreadID] = r.LastSentMessageID
		}
		return m
	}

	// Nothing routed yet → empty list; PUT 404 (the cursor only trails
	// a routed thread).
	s, raw := doJSON(t, http.MethodGet, listURL, gwTok, nil, nil)
	if s != http.StatusOK || len(decodeList(raw)) != 0 {
		t.Fatalf("empty list: %d %s", s, raw)
	}
	if s, _ := doJSON(t, http.MethodPut, cursorURL, gwTok, map[string]any{"message_id": msgID}, nil); s != http.StatusNotFound {
		t.Fatalf("PUT cursor without routing row: %d, want 404", s)
	}

	// Route th on inst-1 (and otherTh on another instance) → the list
	// shows th only, with the empty cursor.
	if err := st.PutTalkGateBinding(ctx, th, "binding-1", "inst-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTalkGateBinding(ctx, otherTh, "binding-2", "inst-2"); err != nil {
		t.Fatal(err)
	}
	s, raw = doJSON(t, http.MethodGet, listURL, gwTok, nil, nil)
	if got := decodeList(raw); s != http.StatusOK || len(got) != 1 || got[th] != "" {
		t.Fatalf("list after routing: %d %s", s, raw)
	}

	// PUT advance guards: empty id, a nonexistent id, and a cross-thread
	// id are all 400 — never silently stored.
	if s, _ := doJSON(t, http.MethodPut, cursorURL, gwTok, map[string]any{"message_id": ""}, nil); s != http.StatusBadRequest {
		t.Fatalf("PUT empty message_id: %d, want 400", s)
	}
	if s, _ := doJSON(t, http.MethodPut, cursorURL, gwTok, map[string]any{"message_id": "does-not-exist"}, nil); s != http.StatusBadRequest {
		t.Fatalf("PUT unknown message_id: %d, want 400", s)
	}
	if s, _ := doJSON(t, http.MethodPut, cursorURL, gwTok, map[string]any{"message_id": otherMsgID}, nil); s != http.StatusBadRequest {
		t.Fatalf("PUT cross-thread message_id: %d, want 400", s)
	}

	// PUT a real message of this thread → 200, and the list reflects it.
	if s, raw := doJSON(t, http.MethodPut, cursorURL, gwTok, map[string]any{"message_id": msgID}, nil); s != http.StatusOK {
		t.Fatalf("PUT valid cursor: %d %s", s, raw)
	}
	_, raw = doJSON(t, http.MethodGet, listURL, gwTok, nil, nil)
	if got := decodeList(raw); got[th] != msgID {
		t.Fatalf("list after advance = %s, want cursor %q", raw, msgID)
	}

	// Wrong scope: RequireScope's uniform 403 on both routes.
	if s, _ := doJSON(t, http.MethodGet, listURL, aliceTok, nil, nil); s != http.StatusForbidden {
		t.Errorf("read/write token GET list: %d, want 403", s)
	}
	if s, _ := doJSON(t, http.MethodPut, cursorURL, aliceTok, map[string]any{"message_id": msgID}, nil); s != http.StatusForbidden {
		t.Errorf("read/write token PUT cursor: %d, want 403", s)
	}
}

// chatList replay stamp (issue #104 G3c): the message-list read honours
// ?author_user_id= ONLY under the gateway scope, so the user-less
// gateway token can replay a talk thread as its owner; every other
// caller sees the query param ignored exactly as before.
func TestChatListGatewayStampReplay(t *testing.T) {
	base, st, gwTok, aliceTok, _ := gwSetup(t)
	ctx := context.Background()
	aliceTh := gwOpenTalkThread(t, st, "alice")
	if _, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: aliceTh, AuthorRole: "human", AuthorUserID: "alice", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	msgsURL := base + "/v1/librarian/threads/" + aliceTh + "/messages"

	// User-less gateway token, no stamp → 404 (can't see the talk thread).
	if s, _ := doJSON(t, http.MethodGet, msgsURL, gwTok, nil, nil); s != http.StatusNotFound {
		t.Fatalf("gateway read without stamp: %d, want 404", s)
	}
	// With the owner stamp in the query → 200 and the message is returned.
	s, raw := doJSON(t, http.MethodGet, msgsURL+"?author_user_id=alice", gwTok, nil, nil)
	if s != http.StatusOK || !strings.Contains(string(raw), "hello") {
		t.Fatalf("gateway stamped read: %d %s", s, raw)
	}
	// Stamped as a non-owner → still 404.
	if s, _ := doJSON(t, http.MethodGet, msgsURL+"?author_user_id=bob", gwTok, nil, nil); s != http.StatusNotFound {
		t.Fatalf("gateway stamped non-owner read: %d, want 404", s)
	}
	// Non-gateway token: the query param is ignored, so bob's token
	// cannot read alice's thread by stamping alice.
	bobTok, err := st.CreateToken(ctx, "bob", "bob-tok", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := doJSON(t, http.MethodGet, msgsURL+"?author_user_id=alice", bobTok, nil, nil); s != http.StatusNotFound {
		t.Fatalf("non-gateway token with stamp query: %d, want 404 (field ignored)", s)
	}
	// The owner reads her own thread normally.
	if s, _ := doJSON(t, http.MethodGet, msgsURL, aliceTok, nil, nil); s != http.StatusOK {
		t.Fatalf("owner read: %d, want 200", s)
	}
}

// The gateway scope resolves to the unrestricted view (nil), mirroring
// admin; a user-less token without it stays pinned to 'internal'.
func TestResolveVisibleSpacesGatewayScope(t *testing.T) {
	_, st, _, _, _ := gwSetup(t)
	ctx := context.Background()

	spaces, err := ResolveVisibleSpaces(ctx, st, &store.APIToken{Scopes: []string{"read", "gateway"}})
	if err != nil {
		t.Fatal(err)
	}
	if spaces != nil {
		t.Errorf("gateway scope view = %v, want nil (unrestricted)", spaces)
	}
	spaces, err = ResolveVisibleSpaces(ctx, st, &store.APIToken{Scopes: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0] != store.SpaceInternal {
		t.Errorf("user-less non-gateway view = %v, want [internal]", spaces)
	}
}

// ---- thread-creation gate routing -----------------------------------

// gateBindServer is testServer with the REAL GateProvisioner (protocol
// 3: no core call), plus alice (active librarian with a gate instance
// id) and bob (no librarian).
func gateBindServer(t *testing.T) (base string, st *store.Store, aliceTok, bobTok, instanceID string) {
	t.Helper()
	dir := t.TempDir()
	var err error
	st, err = store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	for _, u := range []string{"alice", "bob"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	aliceTok, err = st.CreateToken(ctx, "alice", "alice-tok", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bobTok, err = st.CreateToken(ctx, "bob", "bob-tok", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "AliceLib",
	}); err != nil {
		t.Fatal(err)
	}
	instanceID = gate.NewUUIDv7()
	if err := st.SetUserLibrarianGateInstance(ctx, "alice", instanceID); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store:       st,
		Enricher:    enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsEnforce,
		Logger:      logger,
		GateBinder:  opencrab.GateProvisioner{},
	}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, st, aliceTok, bobTok, instanceID
}

func gwCreateThread(t *testing.T, base, tok, intent string) (int, string, []byte) {
	t.Helper()
	s, raw := doJSON(t, http.MethodPost, base+"/v1/librarian/threads", tok,
		map[string]any{"title": "t", "intent": intent}, nil)
	var out struct {
		ThreadID string `json:"thread_id"`
	}
	_ = json.Unmarshal(raw, &out)
	return s, out.ThreadID, raw
}

// Creating a /talk thread for a librarian with a gate instance writes
// the routing row (so the thread goes through the gateway, keyed by the
// thread id); non-talk threads and librarian-less users get none.
func TestTalkThreadCreationBindsGate(t *testing.T) {
	base, st, aliceTok, bobTok, instanceID := gateBindServer(t)
	ctx := context.Background()

	s, threadID, raw := gwCreateThread(t, base, aliceTok, "talk")
	if s != http.StatusCreated || threadID == "" {
		t.Fatalf("open thread: %d %s", s, raw)
	}
	b, err := st.GetTalkGateBinding(ctx, threadID)
	if err != nil {
		t.Fatalf("routing row: %v", err)
	}
	if !gate.IsCanonicalUUID(b.BindingID) || b.InstanceID != instanceID || b.LastSentMessageID != "" {
		t.Errorf("routing row = %+v, want a UUID id, instance %q, empty cursor", b, instanceID)
	}

	_, obsID, _ := gwCreateThread(t, base, aliceTok, "observation")
	if _, err := st.GetTalkGateBinding(ctx, obsID); err == nil {
		t.Error("observation thread got a routing row")
	}
	_, bobID, _ := gwCreateThread(t, base, bobTok, "talk")
	if _, err := st.GetTalkGateBinding(ctx, bobID); err == nil {
		t.Error("librarian-less user's talk thread got a routing row")
	}
	list, err := st.ListTalkGateBindingsByInstance(ctx, instanceID)
	if err != nil || len(list) != 1 || list[0].ThreadID != threadID {
		t.Fatalf("instance routing list = %+v err=%v, want just %q", list, err, threadID)
	}
}
