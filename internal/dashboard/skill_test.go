package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

func TestServeSkillMD(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/skill.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type: %s", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Skill-Version") == "" {
		t.Fatal("missing version header")
	}

	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	// Required content
	for _, want := range []string{
		"name: omoikane",
		"agents/register",
		"kb_lookup_by_trigger",
		"kb_post",
		"kb_feedback",
		"# omoikane",
		"invitation_code",
		"invitation code",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in skill.md", want)
		}
	}

	// BaseURL substitution actually worked
	if !strings.Contains(body, srv.URL) {
		t.Fatalf("baseURL not substituted: %s", body[:500])
	}
}

func TestPublicBase(t *testing.T) {
	cases := []struct {
		host    string
		tls     bool
		proto   string
		want    string
	}{
		{"localhost:8095", false, "", "http://localhost:8095"},
		{"kb.example.com", false, "https", "https://kb.example.com"},
		{"", false, "", "http://localhost:8095"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/skill.md", nil)
		r.Host = c.host
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		got := publicBase(r)
		if got != c.want {
			t.Errorf("host=%q proto=%q: got %q want %q", c.host, c.proto, got, c.want)
		}
	}
}

func TestClaimPagePublic(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &store.Project{ID: "p", Name: "P"})
	reg, _ := s.RegisterAgent(ctx, "test-agent", "for testing")

	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Public access — no auth
	resp, err := http.Get(srv.URL + "/claim/" + reg.ClaimCode)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "test-agent") {
		t.Fatalf("missing agent name: %s", body[:500])
	}
	if !strings.Contains(body, "for testing") {
		t.Fatalf("missing description: %s", body[:500])
	}
}

func TestClaimPageMissing(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/claim/nonexistent")
	defer resp.Body.Close()
	// Page renders but with error message
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "not found or expired") {
		t.Fatalf("expected error message: %s", string(buf[:n]))
	}
}

func TestClaimPageAlreadyClaimed(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &store.User{ID: "alice", Name: "Alice"})
	reg, _ := s.RegisterAgent(ctx, "test-agent", "")
	_ = s.ClaimAgent(ctx, reg.ClaimCode, "alice")

	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/claim/" + reg.ClaimCode)
	defer resp.Body.Close()
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !strings.Contains(body, "Already claimed") {
		t.Fatalf("expected already-claimed banner: %s", body[:800])
	}
}
