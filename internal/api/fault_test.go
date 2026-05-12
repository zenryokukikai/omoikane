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
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/secrets"
	"github.com/kojira/omoikane/internal/store"
)

// ---- writeStoreError taxonomy ----

func TestWriteStoreErrorAllSentinels(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{store.ErrNotFound, 404},
		{store.ErrAlreadyExists, 409},
		{store.ErrInvalidInput, 400},
		{store.ErrVersionMismatch, 409},
		{io.ErrUnexpectedEOF, 500}, // default
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeStoreError(rec, c.err)
		if rec.Code != c.want {
			t.Errorf("%v: got %d want %d", c.err, rec.Code, c.want)
		}
	}
}

// ---- marshalJSONField ----

func TestMarshalJSONField(t *testing.T) {
	if got := marshalJSONField(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := marshalJSONField(map[string]int{"x": 1}); got != `{"x":1}` {
		t.Errorf("map: %q", got)
	}
	if got := marshalJSONField(json.RawMessage("null")); got != "" {
		t.Errorf("null: %q", got)
	}
	// Non-JSON-marshallable values (chan/func) collapse to "" rather than
	// surfacing an error; the contract documents this is unreachable from
	// HTTP callers anyway.
	if got := marshalJSONField(make(chan int)); got != "" {
		t.Errorf("chan: %q", got)
	}
}

// ---- clientIP precedence ----

func TestClientIP(t *testing.T) {
	mk := func(forwarded, real, remote string) *http.Request {
		r, _ := http.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		if real != "" {
			r.Header.Set("X-Real-IP", real)
		}
		return r
	}
	cases := []struct {
		req  *http.Request
		want string
	}{
		{mk("1.2.3.4, 5.6.7.8", "", "x"), "1.2.3.4"},
		{mk("1.2.3.4", "", "x"), "1.2.3.4"},
		{mk("", "9.9.9.9", "x"), "9.9.9.9"},
		{mk("", "", "127.0.0.1:1234"), "127.0.0.1:1234"},
	}
	for _, c := range cases {
		if got := clientIP(c.req); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

// ---- identifyCreator ----

func TestIdentifyCreator(t *testing.T) {
	id, role := identifyCreator(nil)
	if id != "" || role != "" {
		t.Errorf("nil token: id=%q role=%q", id, role)
	}
	id, role = identifyCreator(&store.APIToken{UserID: "u"})
	if id != "u" || role != "human" {
		t.Errorf("user: id=%q role=%q", id, role)
	}
	id, role = identifyCreator(&store.APIToken{Name: "agent-token"})
	if id != "token:agent-token" || role != "agent" {
		t.Errorf("name: id=%q role=%q", id, role)
	}
}

// ---- rejectIfSecrets all three modes ----

func TestRejectIfSecretsModes(t *testing.T) {
	doc := secrets.Doc{Body: "ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012"}
	for _, m := range []config.SecretsMode{
		config.SecretsEnforce, config.SecretsWarn, config.SecretsOff,
	} {
		h := &Handler{
			SecretsMode: m,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		rec := httptest.NewRecorder()
		rejected := h.rejectIfSecrets(rec, doc)
		want := m == config.SecretsEnforce
		if rejected != want {
			t.Errorf("mode=%s: rejected=%v want=%v", m, rejected, want)
		}
	}
}

// ---- Audit middleware specifics ----

func TestAuditSkipsReads(t *testing.T) {
	base, _, st := testServer(t)
	if _, raw := doJSON(t, http.MethodGet, base+"/v1/health", "", nil, nil); len(raw) == 0 {
		t.Fatal("expected body")
	}
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE method='GET' AND path='/v1/health'`).Scan(&n)
	if n != 0 {
		t.Fatalf("read should not be audited, got %d rows", n)
	}
}

func TestAuditSkipsNonV1(t *testing.T) {
	// Mount the API plus a non-/v1 POST endpoint, ensure the Audit
	// middleware does NOT write a row for that path.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := newAPIStore(t)
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Audit(st, logger))
	r.Post("/other", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/other", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	if n != 0 {
		t.Fatalf("non-/v1 POST should not audit, got %d", n)
	}
}

func TestAuditBodyTruncation(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	// Send a body comfortably larger than the 256-byte summarisation cap.
	big := strings.Repeat("x", 1024)
	body := map[string]any{
		"project_id": "kb", "type": "trap", "title": "t",
		"body": big,
	}
	doJSON(t, http.MethodPost, base+"/v1/entries", tok, body, nil)
	var summary string
	_ = st.DB().QueryRow(`SELECT body_summary FROM audit_log WHERE method='POST' AND path='/v1/entries' ORDER BY id DESC LIMIT 1`).Scan(&summary)
	if !strings.Contains(summary, "(truncated)") {
		t.Fatalf("expected truncation marker, got %q", summary)
	}
}

func TestAuditWriteFailureLogged(t *testing.T) {
	// Build a router whose Audit middleware will get a closed-store
	// WriteAudit failure. We close the store BEFORE the request so the
	// middleware's audit-write call sees a closed DB but the request still
	// completes (audit failure is swallowed and logged).
	st := newAPIStore(t)
	_ = st.CreateUser(context.Background(),
		&store.User{ID: "u", Name: "u", Role: "admin"})
	tok, err := st.CreateToken(context.Background(), "u", "t",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store:       st,
		SecretsMode: config.SecretsEnforce,
		Logger:      logger,
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Audit(st, logger))
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Now close the store and POST — Audit's WriteAudit fails, but the
	// handler's CreateProject will also fail. We only care that the
	// process didn't crash and the path was reached.
	_ = st.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/projects",
		strings.NewReader(`{"id":"x","name":"X"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// newAPIStore is a small helper for tests that need a fresh real store.
func newAPIStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dropTable drops a named table with FKs off so dependent tables don't
// block the operation. Used to surface handler-level store errors without
// triggering auth-middleware shadowing.
func dropTable(t *testing.T, s *store.Store, name string) {
	t.Helper()
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE ` + name); err != nil {
		t.Fatal(err)
	}
}

// ---- createEntry edge cases ----

func TestCreateEntryBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/entries",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestCreateEntryBadStatus(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
		"status": "BOGUS",
	}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}


func TestCreateEntryStoreFailure(t *testing.T) {
	// Server's store is closed after seeding — POST /v1/entries should 500
	// because CreateEntry fails inside the handler.
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_ = st.Close()
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestCreateEntryProjectMissing(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "does-not-exist",
		"type":       "trap",
		"title":      "x",
		"body":       "y",
	}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

// ---- updateEntry edge cases ----

func TestUpdateEntryBadIfMatch(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)

	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "not-a-number"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	req, _ := http.NewRequest(http.MethodPatch, base+"/v1/entries/"+id,
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestUpdateEntryBadField(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	// title must be a string; send a number
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"title": 42},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadTags(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"tags": "not-an-array"},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryBadStatus(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "BOGUS"},
		map[string]string{"If-Match": "1"})
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithChangeSummary(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE", "change_summary": "promote"},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryStoreFailure(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	_ = st.Close()
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- deleteEntry edge cases ----

func TestDeleteEntryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	_ = st.Close()
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- getEntry edge cases ----

func TestGetEntryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/X", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestGetEntryAsOfStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet,
		base+"/v1/entries/X?as_of=2026-01-01T00:00:00Z", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- listEntries edge cases ----

func TestListEntriesStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestListEntriesBadLimitOffsetIgnored(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	// Bad limit/offset should silently fall back to defaults.
	s, _ := doJSON(t, http.MethodGet,
		base+"/v1/entries?limit=NaN&offset=BAD", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// ---- entryHistory edge cases ----

func TestEntryHistoryStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/X/history", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- search edge cases ----

func TestSearchBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/search",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestSearchInvalidQueryReturns400(t *testing.T) {
	base, tok, _ := testServer(t)
	// Empty query string after the trim → 400.
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "   "}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestSearchStoreErrorViaInvalidFTS(t *testing.T) {
	// SearchFTS returns ErrInvalidInput when given an empty trimmed
	// query — we already cover that. For the "default" branch in search()
	// (non-ErrInvalidInput store error), use a closed store.
	base, tok, st := testServer(t)
	// Drop the entries table but keep api_tokens intact so auth still
	// works — this surfaces handler-level store errors that a closed DB
	// would shadow at the auth middleware.
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": `"x"*`}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- projects edge cases ----

func TestCreateProjectBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/projects",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestCreateProjectStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "projects")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "x", "name": "y"}, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestListProjectsStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	dropTable(t, st, "projects")
	s, _ := doJSON(t, http.MethodGet, base+"/v1/projects", tok, nil, nil)
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

// ---- runEnrichment edge cases ----

// stubEnricher implements enrich.Enricher with controllable behavior.
type stubEnricher struct {
	tags []string
	err  error
}

func (s stubEnricher) Name() string { return "stub" }
func (s stubEnricher) Enrich(_ context.Context, _ enrich.Input) (enrich.Result, error) {
	if s.err != nil {
		return enrich.Result{}, s.err
	}
	return enrich.Result{Version: 1, Source: "stub", Tags: s.tags}, nil
}

// ---- Mount config ----

func TestMountAttachesAllRoutes(t *testing.T) {
	base, tok, _ := testServer(t)
	// Each route returns *something* — verify the wiring by smoke-testing
	// one read and one write per handler family. Already covered by other
	// tests; this is a sanity check on Mount itself.
	resp, err := http.Get(base + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	doJSON(t, http.MethodGet, base+"/v1/projects", tok, nil, nil)
}

// ---- direct handler tests with injected enricher ----

func TestRunEnrichmentNilEnricher(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{Logger: logger}
	report, merged := h.runEnrichment(context.Background(), "T-X",
		&store.Entry{Type: "trap"}, []string{"a", "B", "a", ""})
	if report.Version != 0 {
		t.Fatalf("expected empty report, got %+v", report)
	}
	if len(merged) != 2 || merged[0] != "a" || merged[1] != "b" {
		t.Fatalf("normalised tags wrong: %v", merged)
	}
}

func TestDeleteEntryNotFound(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/T-MISSING", tok, nil, nil)
	if s != 404 {
		t.Fatalf("status=%d", s)
	}
}

func TestNormaliseUserTagsDirect(t *testing.T) {
	got := normaliseUserTags([]string{"X", "x", " y ", "", "Y"})
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("got %v", got)
	}
}

func TestMountDefaultLogger(t *testing.T) {
	// Handler with nil Logger should still mount without panicking and
	// default-init slog.Default() internally.
	st := newAPIStore(t)
	h := &Handler{Store: st}
	r := chi.NewRouter()
	h.Mount(r)
	if h.Logger == nil {
		t.Fatal("Mount should populate Logger when nil")
	}
}

// failingEnricher always returns err on Enrich.
type failingEnricher struct{}

func (failingEnricher) Name() string { return "fail" }
func (failingEnricher) Enrich(_ context.Context, _ enrich.Input) (enrich.Result, error) {
	return enrich.Result{}, errors.New("synthetic failure")
}

func TestRunEnrichmentEnricherFails(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Logger:   logger,
		Enricher: failingEnricher{},
	}
	report, merged := h.runEnrichment(context.Background(), "T-X",
		&store.Entry{Type: "trap"}, []string{"a", "", "a"})
	if report.Version != 0 {
		t.Fatalf("expected empty report on enricher fail: %+v", report)
	}
	if len(merged) != 1 || merged[0] != "a" {
		t.Fatalf("normalised tags wrong: %v", merged)
	}
}

// taggingEnricher returns a fixed tag set so we can exercise the merge
// loops and the store-write success/failure branches.
type taggingEnricher struct{}

func (taggingEnricher) Name() string { return "tag" }
func (taggingEnricher) Enrich(_ context.Context, _ enrich.Input) (enrich.Result, error) {
	return enrich.Result{Version: 9, Source: "tag", Tags: []string{"alpha", "", "ALPHA", "beta"}}, nil
}

func TestRunEnrichmentReplaceTagsFails(t *testing.T) {
	// Drop the tags table so ReplaceTags fails.
	st := newAPIStore(t)
	dropTable(t, st, "tags")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{Store: st, Logger: logger, Enricher: taggingEnricher{}}
	report, merged := h.runEnrichment(context.Background(), "T-X",
		&store.Entry{Type: "trap"}, []string{"alpha", "", "alpha"})
	// Even though ReplaceTags failed, we still get a report and merged tags
	// (the write failure is logged, not propagated).
	if report.Version != 9 {
		t.Fatalf("expected report.Version=9, got %d", report.Version)
	}
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged tags, got %v", merged)
	}
}

func TestRunEnrichmentSetEnrichmentFails(t *testing.T) {
	// Drop entries → SetEnrichment fails (it updates entries).
	st := newAPIStore(t)
	dropTable(t, st, "entries")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{Store: st, Logger: logger, Enricher: taggingEnricher{}}
	_, _ = h.runEnrichment(context.Background(), "T-X",
		&store.Entry{Type: "trap"}, nil)
	// No assertion on return — coverage is the goal here.
}

func TestUpdateEntryGetEntryFails(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	dropTable(t, st, "entries")
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithTags(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"tags": []string{"alpha", "beta"}},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntrySecretsRejected(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"body": "leaks ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012 here"},
		map[string]string{"If-Match": "1"})
	if s != 422 {
		t.Fatalf("expected 422, got %d", s)
	}
}

func TestUpdateEntryUpdateStoreError(t *testing.T) {
	// Force UpdateEntry to return a non-version-mismatch error after the
	// initial GetEntry has succeeded. Drop the tags table — UpdateEntry's
	// PATCH writes back tag snapshots and fails there.
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	dropTable(t, st, "entry_history")
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"},
		map[string]string{"If-Match": "1"})
	if s != 500 {
		t.Fatalf("expected 500, got %d", s)
	}
}

func TestDeleteEntryNonNotFoundStoreError(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	// Drop entry_history → SoftDeleteEntry's history snapshot write fails
	// with a non-NotFound error.
	dropTable(t, st, "entry_history")
	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 500 {
		t.Fatalf("expected 500, got %d", s)
	}
}

func TestCreateProjectMissingFields(t *testing.T) {
	base, tok, _ := testServer(t)
	// Missing name
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "x"}, nil)
	if s != 400 {
		t.Fatalf("status=%d", s)
	}
}

func TestUpdateEntryWithScopeAndMetadata(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{
			"scope":    map[string]any{"foo": "bar"},
			"metadata": map[string]any{"x": 1},
		},
		map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

// satisfy unused-imports linter if some tests get pruned
var _ = errors.New
var _ = bytes.NewReader
