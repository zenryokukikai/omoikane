package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// getNoRedirect issues a GET that does NOT follow redirects, so a 3xx
// Location can be asserted. The shared get() helper uses http.Get, which
// transparently follows redirects and would mask the login bounce (a 303
// to "/" would otherwise return the home page's 200).
func getNoRedirect(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	u := srv.URL + path
	if token != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + "token=" + url.QueryEscape(token)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// mountAuthedLogin builds a NON-open dashboard (so OptionalAuthenticate
// runs on /login) and returns the server plus a valid session token for
// user "u". The token is passed as ?token= by getNoRedirect, which the
// /login group promotes to a Bearer and OptionalAuthenticate resolves.
func mountAuthedLogin(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s := newDashStore(t)
	tok := issueDashToken(t, s, []string{"read"})
	return mount(t, s, false), tok
}

// mountWithGoogle builds a dashboard test server with the given
// GoogleEnabled state.
func mountWithGoogle(t *testing.T, googleEnabled bool) *httptest.Server {
	t.Helper()
	s := newDashStore(t)
	h, err := New(s, true)
	if err != nil {
		t.Fatal(err)
	}
	h.GoogleEnabled = googleEnabled
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginPageRendersGoogleButton(t *testing.T) {
	srv := mountWithGoogle(t, true)
	code, body := get(t, srv, "/login", "")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(string(body), "Continue with Google") {
		t.Fatalf("missing button: %s", string(body)[:500])
	}
}

func TestLoginPageWhenGoogleDisabled(t *testing.T) {
	srv := mountWithGoogle(t, false)
	_, body := get(t, srv, "/login", "")
	if !strings.Contains(string(body), "KB_OAUTH_GOOGLE_CLIENT_ID") {
		t.Fatalf("missing config hint: %s", string(body)[:500])
	}
}

func TestLoginPageRejectsUnsafeNext(t *testing.T) {
	srv := mountWithGoogle(t, true)
	// External URL should not appear in the rendered <a href>
	_, body := get(t, srv, "/login?next=//evil.com", "")
	if strings.Contains(string(body), "//evil.com") {
		t.Fatalf("external next leaked: %s", string(body))
	}
}

func TestLoginPagePropagatesSafeNext(t *testing.T) {
	srv := mountWithGoogle(t, true)
	_, body := get(t, srv, "/login?next=/entries/T-X", "")
	// html/template emits lower-case hex escapes; accept either casing.
	got := strings.ToLower(string(body))
	if !strings.Contains(got, "next=%2fentries%2ft-x") {
		t.Fatalf("safe next not propagated: %s", string(body))
	}
}

func TestLoginPageShowsError(t *testing.T) {
	srv := mountWithGoogle(t, true)
	_, body := get(t, srv, "/login?error=domain+not+allowed", "")
	if !strings.Contains(string(body), "domain not allowed") {
		t.Fatalf("error not shown: %s", string(body))
	}
}

// --- issue #129: an already-signed-in visitor bounces off /login ---

func TestLoginBounceAuthenticatedWithNext(t *testing.T) {
	srv, tok := mountAuthedLogin(t)
	resp := getNoRedirect(t, srv, "/login?next=/entries/M-CXAJID", tok)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/entries/M-CXAJID" {
		t.Fatalf("Location=%q, want /entries/M-CXAJID", loc)
	}
}

func TestLoginBounceAuthenticatedNoNext(t *testing.T) {
	srv, tok := mountAuthedLogin(t)
	resp := getNoRedirect(t, srv, "/login", tok)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location=%q, want /", loc)
	}
}

func TestLoginBounceAuthenticatedRejectsUnsafeNext(t *testing.T) {
	srv, tok := mountAuthedLogin(t)
	// Both a protocol-relative //host and an absolute https:// URL must be
	// refused as bounce targets and fall back to "/", never the attacker's
	// host (open-redirect guard).
	for _, bad := range []string{"//evil.example", "https://evil.example", `/\evil.example`, `/a\b`, "/a\x08b", "/\x7fevil.example"} {
		resp := getNoRedirect(t, srv, "/login?next="+url.QueryEscape(bad), tok)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("%q: status=%d, want 303", bad, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/" {
			t.Fatalf("%q: Location=%q, want / (must not honour malicious next)", bad, loc)
		}
	}
}

func TestLoginBounceAuthenticatedWithErrorParam(t *testing.T) {
	srv, tok := mountAuthedLogin(t)
	// A signed-in visitor landing on /login?error=… still bounces: the
	// error param is only ever produced for a not-yet-authenticated
	// visitor mid-OAuth, so an authenticated one has nothing to read here.
	resp := getNoRedirect(t, srv, "/login?error=domain+not+allowed&next=/entries/X", tok)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/entries/X" {
		t.Fatalf("Location=%q, want /entries/X", loc)
	}
}

func TestLoginUnauthenticatedRendersFormWithNext(t *testing.T) {
	// NON-open + Google enabled: the form's Google button is the element
	// that echoes ?next, so GoogleEnabled must be on to observe it.
	s := newDashStore(t)
	h, err := New(s, false)
	if err != nil {
		t.Fatal(err)
	}
	h.GoogleEnabled = true
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	// No token: OptionalAuthenticate leaves the request anonymous, so the
	// form must render (200, not a redirect) with the safe next preserved.
	resp := getNoRedirect(t, srv, "/login?next=/entries/T-X", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200 (form, no bounce)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	got := strings.ToLower(string(body))
	if !strings.Contains(got, "next=%2fentries%2ft-x") {
		t.Fatalf("safe next not preserved on the form: %s", string(body))
	}
}

// --- issue #129: the stylesheet must be reachable without a session ---

func TestCSSServedUnauthenticated(t *testing.T) {
	// A NON-open dashboard with no credentials: /static/style.css must
	// still return 200 text/css, otherwise the pre-login pages render as
	// unstyled text (the <link> would 401). Regression guard for the
	// route having lived inside the authenticated group.
	srv := mount(t, newDashStore(t), false)
	resp := getNoRedirect(t, srv, "/static/style.css", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200 unauthenticated", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type=%q, want text/css", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), ":root") {
		t.Fatalf("expected CSS body, got %d bytes", len(body))
	}
}

// Silence net/http import linter if it's not used elsewhere.
var _ = http.MethodGet
