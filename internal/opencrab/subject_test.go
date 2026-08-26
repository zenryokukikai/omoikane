package opencrab

// RuntimeSubjectResolver contract (issue #104): resolve a gate
// subject_id from the runtime's GET /api/agents/{id}, which carries the
// field since upstream opencrab#763. Runtime-side semantics are
// fail-loud: zero mappings → 404, multiple → 409.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

func resolverFor(f *fakeRuntime) *RuntimeSubjectResolver {
	return &RuntimeSubjectResolver{Client: f.client()}
}

// subject_id present and positive → resolved.
func TestSubjectResolverResolves(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"id":"`+agent+`","name":"しおり","subject_id":42}`)

	got, err := resolverFor(f).Resolve(context.Background(), agent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != 42 {
		t.Fatalf("subject_id = %d, want 42", got)
	}
}

// Row parses but has no subject_id field (a runtime that predates it)
// → ErrSubjectUnresolved, so registration stays a logged skip.
func TestSubjectResolverFieldAbsent(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"id":"`+agent+`","name":"しおり"}`)

	_, err := resolverFor(f).Resolve(context.Background(), agent)
	if !errors.Is(err, ErrSubjectUnresolved) {
		t.Fatalf("want ErrSubjectUnresolved, got %v", err)
	}
}

// subject_id present but zero → same as absent.
func TestSubjectResolverZeroSubject(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"id":"`+agent+`","subject_id":0}`)

	_, err := resolverFor(f).Resolve(context.Background(), agent)
	if !errors.Is(err, ErrSubjectUnresolved) {
		t.Fatalf("want ErrSubjectUnresolved, got %v", err)
	}
}

// HTTP 404 (zero mappings / agent row not there yet) →
// ErrSubjectUnresolved — skip, don't fail the save.
func TestSubjectResolver404(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.handlers["GET /api/agents/"+agent] = func(recordedCall) (int, string) {
		return 404, `{"error":"agent not found"}`
	}

	_, err := resolverFor(f).Resolve(context.Background(), agent)
	if !errors.Is(err, ErrSubjectUnresolved) {
		t.Fatalf("want ErrSubjectUnresolved, got %v", err)
	}
}

// Older runtimes answer 200 + JSON null for an absent row — same skip.
func TestSubjectResolverNullRow(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `null`)

	_, err := resolverFor(f).Resolve(context.Background(), agent)
	if !errors.Is(err, ErrSubjectUnresolved) {
		t.Fatalf("want ErrSubjectUnresolved, got %v", err)
	}
}

// HTTP 409 (multiple mappings — fail-loud runtime-side) → a real
// error, NOT ErrSubjectUnresolved: it must surface in the save banner.
func TestSubjectResolver409IsHardError(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.handlers["GET /api/agents/"+agent] = func(recordedCall) (int, string) {
		return 409, `{"error":"multiple subject mappings"}`
	}

	_, err := resolverFor(f).Resolve(context.Background(), agent)
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, ErrSubjectUnresolved) {
		t.Fatalf("409 must not read as unresolved: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("error should carry the status: %v", err)
	}
}

// End-to-end: EnsureInstance with the real resolver against a fake
// runtime + fake gate admin — the instance PUT carries the resolved
// subject_id.
func TestEnsureInstanceWithRuntimeResolver(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"id":"`+agent+`","subject_id":77}`)

	type instancePut struct {
		Path string
		Body map[string]any
	}
	var puts []instancePut
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, "/api/gate-instances/") {
			t.Errorf("unexpected admin call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		puts = append(puts, instancePut{Path: r.URL.Path, Body: m})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		id := strings.TrimPrefix(r.URL.Path, "/api/gate-instances/")
		_, _ = io.WriteString(w, `{"instance_id":"`+id+`"}`)
	}))
	t.Cleanup(admin.Close)

	gp := &GateProvisioner{
		Admin:    gate.NewAdminClient(admin.URL, "operator-token"),
		Resolver: resolverFor(f),
	}
	instanceID, err := gp.EnsureInstance(context.Background(), agent, "")
	if err != nil {
		t.Fatalf("EnsureInstance: %v", err)
	}
	if instanceID == "" {
		t.Fatal("want a registered instance id, got skip")
	}
	if len(puts) != 1 {
		t.Fatalf("instance PUTs = %d, want 1", len(puts))
	}
	p := puts[0]
	if p.Path != "/api/gate-instances/"+instanceID {
		t.Fatalf("PUT path = %s, want /api/gate-instances/%s", p.Path, instanceID)
	}
	if got, _ := p.Body["subject_id"].(float64); int64(got) != 77 {
		t.Fatalf("instance PUT subject_id = %v, want 77", p.Body["subject_id"])
	}
	if p.Body["kind_id"] != GateKindID || p.Body["label"] != agent {
		t.Fatalf("instance PUT kind/label = %v/%v", p.Body["kind_id"], p.Body["label"])
	}
	if p.Body["config_b64"] != base64.StdEncoding.EncodeToString(gateInstanceConfig) {
		t.Fatalf("instance PUT config_b64 = %v", p.Body["config_b64"])
	}
}
