package dashboard

// /my/librarian save flow × external gate registration (issue #104 G2).
// The gate leg runs the REAL opencrab.GateProvisioner and gate.AdminClient
// against a fake admin HTTP server — only the runtime provisioner and
// the subject resolver are faked.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/gate"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// fakeGateAdmin is a minimal admin plane: every PUT answers 201 with a
// body the client can decode, and records method+path+body.
type fakeGateAdmin struct {
	mu    sync.Mutex
	calls []adminHit
}

type adminHit struct {
	Method string
	Path   string
	Body   string
}

func (f *fakeGateAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, adminHit{r.Method, r.URL.Path, string(body)})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/gate-schemas/"):
		_, _ = io.WriteString(w, `{"schema_id":"x","role":"instance_config","format":"json-schema-2020-12","document_b64":"e30=","document_digest":"`+strings.Repeat("a", 64)+`","created_at":1}`)
	case strings.HasPrefix(r.URL.Path, "/api/gate-kinds/"):
		_, _ = io.WriteString(w, `{"kind_id":"omoikane-talk"}`)
	case strings.HasPrefix(r.URL.Path, "/api/gate-instances/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/gate-instances/")
		_, _ = io.WriteString(w, `{"instance_id":"`+id+`"}`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (f *fakeGateAdmin) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method + " " + c.Path
	}
	return out
}

func (f *fakeGateAdmin) instancePuts() []adminHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []adminHit
	for _, c := range f.calls {
		if c.Method == http.MethodPut && strings.HasPrefix(c.Path, "/api/gate-instances/") {
			out = append(out, c)
		}
	}
	return out
}

// fixedResolver resolves every agent to one subject id.
type fixedResolver int64

func (f fixedResolver) Resolve(context.Context, string) (int64, error) {
	return int64(f), nil
}

// mountLibrarianGate is mountLibrarian + a gate provisioner wired to a
// fake admin server. resolver nil → the real stub resolver.
func mountLibrarianGate(t *testing.T, resolver opencrab.SubjectResolver) (*httptest.Server, *store.Store, string, *fakeGateAdmin) {
	t.Helper()
	admin := &fakeGateAdmin{}
	adminSrv := httptest.NewServer(admin)
	t.Cleanup(adminSrv.Close)

	if resolver == nil {
		resolver = opencrab.StubSubjectResolver{}
	}
	gp := &opencrab.GateProvisioner{
		Admin:    gate.NewAdminClient(adminSrv.URL, "op-token"),
		Resolver: resolver,
	}

	s := newDashStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &store.User{
		ID: "alice", Name: "Alice", Role: "admin", Email: "alice@x.com",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateToken(ctx, "alice", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(s, false)
	if err != nil {
		t.Fatal(err)
	}
	h.Librarian = &fakeProvisioner{}
	h.Gate = gp
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s, tok, admin
}

// Gate feature off (h.Gate == nil): save succeeds and no gate state
// appears.
func TestMyLibrarianSaveGateOff(t *testing.T) {
	srv, s, tok := mountLibrarian(t, &fakeProvisioner{})
	code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: HTTP %d, want 303", code)
	}
	ul, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "" {
		t.Errorf("gate_instance_id = %q, want empty (feature off)", ul.GateInstanceID)
	}
}

// Gate on + stub resolver: kind/schemas register, instance is SKIPPED
// (subject lookup is upstream work), the save still succeeds and
// gate_instance_id stays empty.
func TestMyLibrarianSaveGateStubResolverSkips(t *testing.T) {
	srv, s, tok, admin := mountLibrarianGate(t, nil)
	code, body := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: HTTP %d, want 303 — body: %s", code, body)
	}
	ul, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "" {
		t.Errorf("gate_instance_id = %q, want empty (skip path)", ul.GateInstanceID)
	}
	paths := admin.paths()
	wantPrefix := []string{
		"PUT /api/gate-schemas/omoikane-talk-config",
		"PUT /api/gate-schemas/omoikane-talk-secrets",
		"PUT /api/gate-kinds/omoikane-talk",
	}
	if len(paths) != len(wantPrefix) {
		t.Fatalf("admin calls = %v, want exactly %v", paths, wantPrefix)
	}
	for i := range wantPrefix {
		if paths[i] != wantPrefix[i] {
			t.Errorf("admin call %d = %q, want %q", i, paths[i], wantPrefix[i])
		}
	}
}

// Gate on + working resolver: the instance PUT reaches the admin plane
// with the resolved subject_id, the id persists, and a second save
// neither re-registers the kind nor mints a second instance.
func TestMyLibrarianSaveGateRegistersInstance(t *testing.T) {
	srv, s, tok, admin := mountLibrarianGate(t, fixedResolver(42))
	code, body := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: HTTP %d, want 303 — body: %s", code, body)
	}

	puts := admin.instancePuts()
	if len(puts) != 1 {
		t.Fatalf("instance PUTs = %d, want 1 (%v)", len(puts), admin.paths())
	}
	var req struct {
		KindID    string `json:"kind_id"`
		Label     string `json:"label"`
		SubjectID int64  `json:"subject_id"`
		Enabled   bool   `json:"enabled"`
		ConfigB64 string `json:"config_b64"`
	}
	if err := json.Unmarshal([]byte(puts[0].Body), &req); err != nil {
		t.Fatal(err)
	}
	if req.KindID != "omoikane-talk" || req.Label != "plib-alice" ||
		req.SubjectID != 42 || !req.Enabled || req.ConfigB64 != "e30=" {
		t.Errorf("instance PUT body: %s", puts[0].Body)
	}

	ul, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantID := strings.TrimPrefix(puts[0].Path, "/api/gate-instances/")
	if ul.GateInstanceID == "" || ul.GateInstanceID != wantID {
		t.Errorf("gate_instance_id = %q, want %q (the PUT path id)", ul.GateInstanceID, wantID)
	}

	// Second save: registration is latched, the existing instance id is
	// reused — zero further admin calls.
	before := len(admin.paths())
	code, body = postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib renamed"})
	if code != http.StatusSeeOther {
		t.Fatalf("second save: HTTP %d, want 303 — body: %s", code, body)
	}
	if after := len(admin.paths()); after != before {
		t.Errorf("second save made %d extra admin calls: %v", after-before, admin.paths()[before:])
	}
	ul2, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul2.GateInstanceID != ul.GateInstanceID {
		t.Errorf("gate_instance_id changed across saves: %q → %q", ul.GateInstanceID, ul2.GateInstanceID)
	}
}

// Gate admin unreachable: the save fails visibly (502-style banner),
// and the librarian row is not written half-way.
func TestMyLibrarianSaveGateAdminDown(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":"store_error","at":null,"detail":null}}`)
	}))
	t.Cleanup(admin.Close)

	s := newDashStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &store.User{
		ID: "alice", Name: "Alice", Role: "admin", Email: "alice@x.com",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateToken(ctx, "alice", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(s, false)
	if err != nil {
		t.Fatal(err)
	}
	h.Librarian = &fakeProvisioner{}
	h.Gate = &opencrab.GateProvisioner{
		Admin:    gate.NewAdminClient(admin.URL, "op-token"),
		Resolver: opencrab.StubSubjectResolver{},
	}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	code, body := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusBadGateway {
		t.Fatalf("save: HTTP %d, want 502 — body: %s", code, body)
	}
	if !strings.Contains(body, "外部ゲートへの登録に失敗しました") {
		t.Errorf("error banner missing from body")
	}
}
