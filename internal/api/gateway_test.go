package api

// Gateway scope (issue #104 G3a, C案): author attribution on chat +
// broadcast, the /v1/gateway/librarians roster, SSE-equivalent
// visibility, and the thread-creation gate-binding hook (run against
// the REAL opencrab.GateProvisioner + gate.AdminClient and a fake
// admin HTTP server, the same construction as
// dashboard/my_librarian_gate_test.go).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// ---- thread-creation gate binding -----------------------------------

// fakeBindingAdmin is a minimal admin plane for the binding PUT: it
// records calls and answers 201 (or a uniform 500 when failing).
type fakeBindingAdmin struct {
	mu    sync.Mutex
	fail  bool
	calls []adminBindingHit
}

type adminBindingHit struct {
	Method, Path, Body string
}

func (f *fakeBindingAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, adminBindingHit{r.Method, r.URL.Path, string(body)})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if f.fail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":"store_error","at":null,"detail":null}}`)
		return
	}
	w.WriteHeader(http.StatusCreated)
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/gate-bindings/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/gate-bindings/")
		_, _ = io.WriteString(w, `{"binding_id":"`+id+`"}`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (f *fakeBindingAdmin) bindingPuts() []adminBindingHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []adminBindingHit
	for _, c := range f.calls {
		if c.Method == http.MethodPut && strings.HasPrefix(c.Path, "/api/gate-bindings/") {
			out = append(out, c)
		}
	}
	return out
}

// gateBindServer is testServer with the REAL GateProvisioner wired to
// the fake admin, plus alice (active librarian, registered instance)
// and bob (no librarian).
func gateBindServer(t *testing.T, fail bool) (base string, st *store.Store, aliceTok, bobTok, instanceID string, admin *fakeBindingAdmin) {
	t.Helper()
	admin = &fakeBindingAdmin{fail: fail}
	adminSrv := httptest.NewServer(admin)
	t.Cleanup(adminSrv.Close)

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
		GateBinder: &opencrab.GateProvisioner{
			Admin: gate.NewAdminClient(adminSrv.URL, "op-token"),
			Log:   logger,
		},
	}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, st, aliceTok, bobTok, instanceID, admin
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

// Happy path: creating a /talk thread PUTs one binding (address = the
// thread id, empty metadata, null catch-up) and stores the row.
func TestTalkThreadCreationBindsGate(t *testing.T) {
	base, st, aliceTok, bobTok, instanceID, admin := gateBindServer(t, false)

	s, threadID, raw := gwCreateThread(t, base, aliceTok, "talk")
	if s != http.StatusCreated || threadID == "" {
		t.Fatalf("open thread: %d %s", s, raw)
	}

	puts := admin.bindingPuts()
	if len(puts) != 1 {
		t.Fatalf("binding PUTs = %d (%+v), want 1", len(puts), admin.calls)
	}
	var req struct {
		InstanceID         string  `json:"instance_id"`
		Address            string  `json:"address"`
		Label              *string `json:"label"`
		BindingMetadataB64 string  `json:"binding_metadata_b64"`
		CatchUpStart       any     `json:"catch_up_start"`
	}
	if err := json.Unmarshal([]byte(puts[0].Body), &req); err != nil {
		t.Fatal(err)
	}
	if req.InstanceID != instanceID || req.Address != threadID ||
		req.BindingMetadataB64 != "e30=" || req.CatchUpStart != nil {
		t.Errorf("binding PUT body: %s", puts[0].Body)
	}
	wantBindingID := strings.TrimPrefix(puts[0].Path, "/api/gate-bindings/")

	b, err := st.GetTalkGateBinding(context.Background(), threadID)
	if err != nil {
		t.Fatalf("binding row: %v", err)
	}
	if b.BindingID != wantBindingID || b.InstanceID != instanceID || b.LastSentMessageID != "" {
		t.Errorf("binding row = %+v, want binding %q instance %q empty cursor", b, wantBindingID, instanceID)
	}

	// Non-talk threads and librarian-less users never touch the gate.
	before := len(admin.bindingPuts())
	if s, _, raw := gwCreateThread(t, base, aliceTok, "observation"); s != http.StatusCreated {
		t.Fatalf("observation thread: %d %s", s, raw)
	}
	if s, _, raw := gwCreateThread(t, base, bobTok, "talk"); s != http.StatusCreated {
		t.Fatalf("bob talk thread: %d %s", s, raw)
	}
	if after := len(admin.bindingPuts()); after != before {
		t.Errorf("unexpected binding PUTs: %+v", admin.bindingPuts()[before:])
	}
}

// Admin-plane failure: thread creation still succeeds; no row appears
// (best-effort — the /talk flow works without the gate).
func TestTalkThreadCreationGateFailureNonFatal(t *testing.T) {
	base, st, aliceTok, _, _, admin := gateBindServer(t, true)

	s, threadID, raw := gwCreateThread(t, base, aliceTok, "talk")
	if s != http.StatusCreated || threadID == "" {
		t.Fatalf("open thread with failing admin: %d %s, want 201", s, raw)
	}
	if len(admin.bindingPuts()) != 1 {
		t.Fatalf("binding PUTs = %d, want the one failed attempt", len(admin.bindingPuts()))
	}
	if _, err := st.GetTalkGateBinding(context.Background(), threadID); err == nil {
		t.Error("binding row stored despite admin failure")
	}
}
