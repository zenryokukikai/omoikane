package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zenryokukikai/omoikane/internal/config"
	"github.com/zenryokukikai/omoikane/internal/dashboard"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// setupEnv points KB_DB_PATH to a temp file and resets relevant env vars
// for hermetic config.Load() calls.
func setupEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kb.db")
	t.Setenv("KB_DB_PATH", dbPath)
	t.Setenv("KB_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KB_DASHBOARD_OPEN", "1")
	t.Setenv("KB_LLM_PROVIDER", "")
	t.Setenv("KB_SECRETS_MODE", "enforce")
	return dbPath
}

func TestUsage(t *testing.T) {
	buf := &bytes.Buffer{}
	Usage(buf)
	if !bytes.Contains(buf.Bytes(), []byte("kb-server")) {
		t.Fatalf("usage missing app name: %q", buf.String())
	}
}

func TestRunVersion(t *testing.T) {
	out := &bytes.Buffer{}
	if code := Run([]string{"version"}, out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("kb-server")) {
		t.Fatalf("output: %q", out.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		out := &bytes.Buffer{}
		if code := Run([]string{flag}, out, &bytes.Buffer{}); code != 0 {
			t.Fatalf("%s code=%d", flag, code)
		}
		if !bytes.Contains(out.Bytes(), []byte("usage")) {
			t.Fatalf("%s output: %q", flag, out.String())
		}
	}
}

func TestRunAdminTokenViaRun(t *testing.T) {
	setupEnv(t)
	out := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := Run([]string{"admin-token", "--user", "u1", "--scopes", "read"},
		out, stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "admin-token") {
		t.Fatalf("missing header: %s", out.String())
	}
}

func TestAdminTokenWithExistingUser(t *testing.T) {
	dbPath := setupEnv(t)
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "preexists", Name: "pre", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := &bytes.Buffer{}
	code := AdminToken([]string{"--user", "preexists", "--name", "t1"},
		out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), "USER  : preexists") {
		t.Fatalf("missing existing-user output: %s", out.String())
	}
}

func TestAdminTokenWithTTL(t *testing.T) {
	setupEnv(t)
	out := &bytes.Buffer{}
	code := AdminToken([]string{"--user", "u", "--ttl", "5m"}, out, &bytes.Buffer{})
	if code != 0 {
		t.Fatal("expected success")
	}
	if !strings.Contains(out.String(), "EXPIRY:") {
		t.Fatalf("expected EXPIRY line: %s", out.String())
	}
}

func TestAdminTokenBadFlag(t *testing.T) {
	setupEnv(t)
	code := AdminToken([]string{"--nope"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestAdminTokenConfigError(t *testing.T) {
	t.Setenv("KB_DB_PATH", "")
	t.Setenv("KB_SECRETS_MODE", "bogus") // config.Load will reject this
	stderr := &bytes.Buffer{}
	code := AdminToken(nil, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "config") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestAdminTokenOpenStoreError(t *testing.T) {
	t.Setenv("KB_DB_PATH", "/no/such/directory/kb.db")
	t.Setenv("KB_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KB_SECRETS_MODE", "enforce")
	stderr := &bytes.Buffer{}
	code := AdminToken(nil, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "open store") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

// fakeAdminStore implements AdminStorer with configurable failures.
type fakeAdminStore struct {
	getUserErr    error
	createUserErr error
	createTokErr  error
	setEmailErr   error
	user          *store.User
	closeCalled   bool
}

func (f *fakeAdminStore) GetUser(_ context.Context, _ string) (*store.User, error) {
	return f.user, f.getUserErr
}
func (f *fakeAdminStore) CreateUser(_ context.Context, _ *store.User) error {
	return f.createUserErr
}
func (f *fakeAdminStore) CreateToken(_ context.Context, _, _ string, _ []string, _ *time.Time) (string, error) {
	if f.createTokErr != nil {
		return "", f.createTokErr
	}
	return "plain-token-abc", nil
}
func (f *fakeAdminStore) SetUserEmail(_ context.Context, _, _ string) error {
	return f.setEmailErr
}
func (f *fakeAdminStore) Close() error { f.closeCalled = true; return nil }

func TestRunAdminTokenGetUserUnknownError(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := runAdminToken(context.Background(),
		&fakeAdminStore{getUserErr: io.ErrUnexpectedEOF},
		"u", "n", "admin", "read", "", 0, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "lookup user") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestRunAdminTokenCreateUserError(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := runAdminToken(context.Background(),
		&fakeAdminStore{
			getUserErr:    store.ErrNotFound,
			createUserErr: io.ErrUnexpectedEOF,
		},
		"u", "n", "admin", "read", "", 0, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "create user") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestRunAdminTokenCreateTokenError(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := runAdminToken(context.Background(),
		&fakeAdminStore{
			getUserErr:   store.ErrNotFound,
			createTokErr: io.ErrUnexpectedEOF,
		},
		"u", "n", "admin", "read", "", 0, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "create token") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestAdminTokenOpenStoreOverride(t *testing.T) {
	// Drive AdminToken() through to runAdminToken via the overridable
	// openAdminStore hook, covering the success path and the close defer.
	setupEnv(t)
	orig := openAdminStore
	t.Cleanup(func() { openAdminStore = orig })
	fake := &fakeAdminStore{getUserErr: store.ErrNotFound}
	openAdminStore = func(_ context.Context, _ string) (AdminStorer, error) { return fake, nil }
	out := &bytes.Buffer{}
	if code := AdminToken(nil, out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !fake.closeCalled {
		t.Fatal("store.Close not called")
	}
	if !strings.Contains(out.String(), "plain-token-abc") {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestBuildRouterDashboardError(t *testing.T) {
	setupEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	orig := newDashboard
	t.Cleanup(func() { newDashboard = orig })
	newDashboard = func(*store.Store, bool) (*dashboard.Handler, error) {
		return nil, io.ErrUnexpectedEOF
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := BuildRouter(st, cfg, logger); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunServerBuildRouterError(t *testing.T) {
	// Cover the runServer "BuildRouter failed" branch via the
	// newDashboard override.
	setupEnv(t)
	orig := newDashboard
	t.Cleanup(func() { newDashboard = orig })
	newDashboard = func(*store.Store, bool) (*dashboard.Handler, error) {
		return nil, io.ErrUnexpectedEOF
	}
	stderr := &bytes.Buffer{}
	code := Run(nil, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "fatal") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestRunUnknownArgRunsServer(t *testing.T) {
	// Any unknown first arg falls through to runServer, which will then
	// fail because the listener port can't be bound (we use a deliberately
	// invalid address). This covers the "fall through to server" branch.
	t.Setenv("KB_HTTP_ADDR", "256.256.256.256:99999")
	t.Setenv("KB_DB_PATH", filepath.Join(t.TempDir(), "kb.db"))
	t.Setenv("KB_SECRETS_MODE", "enforce")
	stderr := &bytes.Buffer{}
	code := Run(nil, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "fatal") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestBuildRouter(t *testing.T) {
	setupEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := BuildRouter(st, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	// /v1/health is public — verify the wiring.
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/health", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBuildRouterDashboardOpen(t *testing.T) {
	setupEnv(t)
	t.Setenv("KB_DASHBOARD_OPEN", "1")
	t.Setenv("KB_REQUEST_BODY_MAX", "0") // exercise the no-LimitBody branch
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := BuildRouter(st, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dashboard root: status=%d", rec.Code)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV("a, , b ,c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("splitCSV: %v", got)
	}
	if len(splitCSV("")) != 0 {
		t.Fatal("empty should return empty slice")
	}
}

func TestListenAndServe(t *testing.T) {
	// We spin up a real listener on an ephemeral port, call Serve, then
	// shut down. This covers ListenAndServe's success-then-graceful path.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})}
	done := make(chan error, 1)
	go func() { done <- ListenAndServe(srv, ln) }()

	// Give Serve a moment to enter the accept loop, then shutdown.
	time.Sleep(50 * time.Millisecond)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
}

func TestListenAndServeError(t *testing.T) {
	// Pass a listener that's already closed → Serve returns a non-EOF
	// error (we map it through ListenAndServe).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	err = ListenAndServe(&http.Server{Handler: http.NotFoundHandler()}, ln)
	if err == nil {
		t.Fatal("expected error from closed listener")
	}
}

// runServer is exercised end-to-end by starting it in a goroutine, hitting
// /v1/health on the bound port, and then sending SIGTERM. We can't send
// real signals in unit tests easily; instead we use a private helper to
// trigger graceful shutdown via context cancellation.

func TestRunServerHappyPath(t *testing.T) {
	dbPath := setupEnv(t)
	// Bind a free port for the test process.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("KB_HTTP_ADDR", addr)
	t.Setenv("KB_DB_PATH", dbPath)

	stdout := &bytes.Buffer{}
	done := make(chan int, 1)
	go func() {
		done <- Run(nil, stdout, &bytes.Buffer{})
	}()

	// Wait for the server to be ready, then send SIGTERM to ourselves.
	waitForHealth(t, "http://"+addr)
	proc, _ := os.FindProcess(os.Getpid())
	// SIGINT triggers the graceful shutdown path; on macOS/linux this is
	// safe in tests since signal.NotifyContext is scoped to runServer.
	_ = proc.Signal(os.Interrupt)

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Run returned %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown signal")
	}
}

func waitForHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func TestRunServerConfigError(t *testing.T) {
	t.Setenv("KB_SECRETS_MODE", "bogus")
	code := Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestRunServerStoreError(t *testing.T) {
	t.Setenv("KB_DB_PATH", "/no/such/dir/kb.db")
	t.Setenv("KB_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KB_SECRETS_MODE", "enforce")
	code := Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

// jsonBody helper for any future server tests that need to decode JSON.
func jsonBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// _ keeps unused helpers around for future tests.
var _ = jsonBody
