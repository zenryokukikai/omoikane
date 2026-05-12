package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

// mountAuthed mounts a dashboard that requires auth (Open=false). The
// returned token can be used as `?token=<tok>` on the request URL to
// authenticate as the bootstrapped admin user.
func mountAuthed(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
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
	h, err := New(s, false) // Open=false → auth required
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s, tok
}

func TestAgentsPageRequiresAuth(t *testing.T) {
	srv, _, _ := mountAuthed(t)
	resp, err := http.Get(srv.URL + "/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// No auth → 401 or redirect to /login. Both are acceptable for this UX.
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 401 or 302, got %d", resp.StatusCode)
	}
}

func TestAgentsPageWithAuth(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	ctx := context.Background()

	// Seed some state: an invite + an adopted agent
	inv, _ := st.CreateAgentInvitation(ctx, "alice", "test invite")
	reg, _ := st.RedeemAgentInvitation(ctx, inv.Code, "test-agent", "doing tests")
	_ = reg

	resp, err := http.Get(srv.URL + "/agents?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"Your agents",
		"Issue a new invitation",
		"test invite",
		"test-agent",
		"alice@x.com",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in /agents", want)
		}
	}
}

func TestAgentsIssueForm(t *testing.T) {
	srv, st, tok := mountAuthed(t)

	form := url.Values{}
	form.Set("note", "for the lipsync agent")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/agents/issue?token="+tok, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "New invitation issued") {
		t.Fatalf("missing success banner: %s", string(body)[:500])
	}

	// Verify the invite was persisted under alice
	invs, _ := st.ListAgentInvitations(context.Background(), "alice")
	if len(invs) != 1 || invs[0].Note != "for the lipsync agent" {
		t.Fatalf("invitations: %+v", invs)
	}
	// And the code visible on the page actually matches the persisted one
	if !strings.Contains(string(body), invs[0].Code) {
		t.Fatalf("code not displayed: %s", invs[0].Code)
	}
}

func TestAgentsIssueWithoutAuthRedirects(t *testing.T) {
	srv, _, _ := mountAuthed(t)
	form := url.Values{}
	form.Set("note", "x")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/agents/issue", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Either 401 (token middleware) or 302 (handler bouncing to /login).
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect/401, got %d", resp.StatusCode)
	}
}

// Sanity: the home page now exposes a link to /agents in the subnav.
func TestHomeSubnavLinksToAgents(t *testing.T) {
	srv, _, tok := mountAuthed(t)
	resp, _ := http.Get(srv.URL + "/?token=" + tok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `href="/agents`) {
		t.Fatalf("subnav missing /agents link: %s", string(body)[:500])
	}
}

// The global header should show the signed-in user's name/email and a
// link to /agents on every page (so code issuance is discoverable from
// anywhere in the dashboard). This locks the rendering of pc.Me in
// renderCtx + the {{if .Me}} block in layout.html.
func TestGlobalHeaderShowsUserAndInviteLink(t *testing.T) {
	srv, _, tok := mountAuthed(t)
	// Hit the home page — but the markup we care about is in the layout
	// header, so any page that uses the layout would do.
	resp, _ := http.Get(srv.URL + "/?token=" + tok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	for _, want := range []string{
		`class="header-invite"`,    // 🎟️ Invite chip (now a submit button)
		`action="/agents/issue`,    // chip is a form — clicking issues a code
		`class="header-user"`,      // User pill with avatar+name
		`class="header-user-name"`, // visible name span
		"alice@x.com",              // the bootstrapped user's email
		`href="/agents`,            // user pill links to /agents
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in home page header", want)
		}
	}
}

// Time-related: invites with past expiry should still be listed (just
// marked unused — the dashboard shows them so the human sees what
// they've issued historically). We can't easily backdate without
// touching the DB directly; this test just verifies that the listing
// returns the expected row.
func TestAgentsListMultiple(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	ctx := context.Background()
	_, _ = st.CreateAgentInvitation(ctx, "alice", "first")
	_, _ = st.CreateAgentInvitation(ctx, "alice", "second")
	_, _ = st.CreateAgentInvitation(ctx, "alice", "third")
	// Also one from a different user — should NOT appear
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob"})
	_, _ = st.CreateAgentInvitation(ctx, "bob", "bobs invite — should not show")

	resp, _ := http.Get(srv.URL + "/agents?token=" + tok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing alice's invite note %q", want)
		}
	}
	if strings.Contains(string(body), "bobs invite — should not show") {
		t.Fatal("bob's invite leaked into alice's view")
	}
	_ = time.Hour
}
