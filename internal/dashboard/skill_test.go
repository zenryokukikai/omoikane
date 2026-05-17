package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

// /skill.md is the ONE canonical endpoint. It serves the full Agent
// Skills spec (frontmatter, auth, API contract, chat ping-pong
// protocol, error handling). Agents fetch it once and have
// everything. Earlier history: there was a separate
// /skills/omoikane/SKILL.md serving the same content + a /skill.md
// that pointed at it. Both duplicated and drifted. Now: one URL.
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

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Agent-Skills-standard frontmatter first
	if !strings.HasPrefix(body, "---\nname: omoikane\n") {
		t.Fatalf("missing frontmatter: %s", body[:200])
	}
	// Full content lives here now — auth, tools, chat protocol,
	// attachment usage.
	for _, want := range []string{
		"description:",
		"invitation_code",
		"by-trigger", // lookup endpoint
		"by-symptom",
		"prohibited",
		"Pseudo-realtime ping-pong",         // chat protocol section
		"long-poll",                         // long-poll cursor pattern
		"Loop prevention",                   // loop guardrails
		"Attachments — evidence",            // attachment section header
		"POST <base-url>/v1/attachments",    // upload usage
		"attached:<id>",                     // body-reference syntax
		"worst-case",                        // role vocabulary present
		"When to use chat (vs entry)",       // directive chat-vs-entry section
		"DO NOT use chat for",               // negative directives spelled out
		"Status reports of training",        // the lipsync-induced specific anti-example
		"Searching chat (opt-in)",           // chat search opt-in section
		`"include_chat":true`,               // opt-in flag documentation
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /skill.md (canonical full spec)", want)
		}
	}
}

// The previous /skills/omoikane/SKILL.md route was removed (a single
// /skill.md is the canonical endpoint now). This test locks that
// the route doesn't quietly come back via copy-paste from old docs.
func TestNoLegacySkillsOmoikaneRoute(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/skills/omoikane/SKILL.md")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/skills/omoikane/SKILL.md should be 404 (replaced by /skill.md), got %d", resp.StatusCode)
	}
}

// /skills/install.sh used to return a `mkdir + curl` shell script
// intended to be piped to `sh`. That's a security anti-pattern
// (silent arbitrary-code execution against whatever URL serves) AND
// it overreached by prescribing a specific host path. Both reasons
// for removal; this test locks the absence so it doesn't sneak back.
func TestNoInstallShRoute(t *testing.T) {
	s := newDashStore(t)
	h, _ := New(s, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/skills/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/skills/install.sh should be 404, got %d (a curl|sh entry point is a security anti-pattern)", resp.StatusCode)
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
