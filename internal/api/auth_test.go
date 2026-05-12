package api

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

	"github.com/kojira/omoikane/internal/auth/oauth"
	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/store"
)

// fakeGoogleOAuth replays scripted Identity / error for tests.
type fakeGoogleOAuth struct {
	identity *oauth.Identity
	authErr  error
	cbErr    error
}

func (f *fakeGoogleOAuth) Name() string { return "google" }
func (f *fakeGoogleOAuth) Authorize(state string) string {
	return "https://fake.google/auth?state=" + state
}
func (f *fakeGoogleOAuth) Callback(ctx context.Context, code string) (*oauth.Identity, error) {
	if f.cbErr != nil {
		return nil, f.cbErr
	}
	return f.identity, nil
}

// oauthServer builds a Handler wired with the fake provider.
func oauthServer(t *testing.T, fake *fakeGoogleOAuth, allowDomains []string) (base, tok string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	var err error
	st, err = store.Open(context.Background(), filepath.Join(dir, "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "admin", Name: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, err = st.CreateToken(context.Background(), "admin", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store: st, Enricher: enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsOff, Logger: logger,
		AuthAllowDomains: allowDomains,
	}
	// Assign via the interface-conversion branch so typed-nil isn't
	// stored as a non-nil interface.
	if fake != nil {
		h.OAuthGoogle = fake
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Audit(st, logger))
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, tok, st
}

func TestAuthGoogleLoginRedirectsToProvider(t *testing.T) {
	base, _, _ := oauthServer(t, &fakeGoogleOAuth{}, nil)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Get(base + "/v1/auth/google/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "https://fake.google/auth?state=") {
		t.Fatalf("location: %s", loc)
	}
	// State cookie must be set
	cookieFound := false
	for _, c := range resp.Cookies() {
		if c.Name == stateCookieName && c.Value != "" {
			cookieFound = true
		}
	}
	if !cookieFound {
		t.Fatal("state cookie not set")
	}
}

func TestAuthGoogleLoginNotConfigured(t *testing.T) {
	base, _, _ := oauthServer(t, nil, nil)
	resp, err := http.Get(base + "/v1/auth/google/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackHappyPath(t *testing.T) {
	fake := &fakeGoogleOAuth{
		identity: &oauth.Identity{
			Provider: "google", Subject: "sub-1",
			Email: "alice@company.com", Name: "Alice",
		},
	}
	base, _, st := oauthServer(t, fake, []string{"company.com"})

	// First call /login to get a state cookie
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, err := c.Get(base + "/v1/auth/google/login")
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	var stateCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == stateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("no state cookie")
	}
	state := strings.SplitN(stateCookie.Value, ":", 2)[0]

	// Now hit the callback with the matching state + a code
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=ABC&state="+state, nil)
	req.AddCookie(stateCookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	// Session cookie issued
	hasSession := false
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			hasSession = true
		}
	}
	if !hasSession {
		t.Fatal("session cookie not set")
	}

	// User got provisioned
	u, err := st.GetUserByEmail(context.Background(), "alice@company.com")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if u.GoogleSub != "sub-1" {
		t.Fatalf("google_sub: %s", u.GoogleSub)
	}
	if u.LastLoginAt == nil {
		t.Fatal("last_login_at should be set")
	}
}

func TestAuthGoogleCallbackStateMismatch(t *testing.T) {
	base, _, _ := oauthServer(t, &fakeGoogleOAuth{identity: &oauth.Identity{Subject: "x"}},
		nil)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=x&state=wrong", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "actual-state:/"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackMissingCookie(t *testing.T) {
	base, _, _ := oauthServer(t, &fakeGoogleOAuth{}, nil)
	resp, err := http.Get(base + "/v1/auth/google/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackDeniedByAllowList(t *testing.T) {
	fake := &fakeGoogleOAuth{
		identity: &oauth.Identity{Subject: "s", Email: "outsider@evil.com", Name: "X"},
	}
	base, _, _ := oauthServer(t, fake, []string{"company.com"})
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=x&state="+state, nil)
	req.AddCookie(sc)
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackProviderError(t *testing.T) {
	fake := &fakeGoogleOAuth{cbErr: io.ErrUnexpectedEOF}
	base, _, _ := oauthServer(t, fake, nil)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=x&state="+state, nil)
	req.AddCookie(sc)
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackMissingCode(t *testing.T) {
	base, _, _ := oauthServer(t, &fakeGoogleOAuth{}, nil)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?state="+state, nil)
	req.AddCookie(sc)
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthGoogleCallbackProviderReportedError(t *testing.T) {
	base, _, _ := oauthServer(t, &fakeGoogleOAuth{}, nil)
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?error=access_denied&state="+state, nil)
	req.AddCookie(sc)
	resp, _ := c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthMeViaSessionCookie(t *testing.T) {
	fake := &fakeGoogleOAuth{
		identity: &oauth.Identity{Subject: "s", Email: "alice@x.com", Name: "Alice"},
	}
	base, _, _ := oauthServer(t, fake, nil)

	// Complete the flow to get a session cookie
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=x&state="+state, nil)
	req.AddCookie(sc)
	cbResp, _ := c.Do(req)
	cbResp.Body.Close()
	var sessionCookie *http.Cookie
	for _, cc := range cbResp.Cookies() {
		if cc.Name == sessionCookieName {
			sessionCookie = cc
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}

	// Call /auth/me with the cookie (no Authorization header)
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/auth/me", nil)
	req.AddCookie(sessionCookie)
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("/auth/me: %d", meResp.StatusCode)
	}
	var out struct {
		User  map[string]any `json:"user"`
		Token map[string]any `json:"token"`
	}
	_ = json.NewDecoder(meResp.Body).Decode(&out)
	if out.User["email"] != "alice@x.com" {
		t.Fatalf("me user: %+v", out.User)
	}
	if out.Token["type"] != "session" {
		t.Fatalf("token type: %v", out.Token["type"])
	}
}

func TestAuthMeUnauthenticated(t *testing.T) {
	base, _, _ := oauthServer(t, nil, nil)
	resp, _ := http.Get(base + "/v1/auth/me")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestAuthLogoutRevokesSession(t *testing.T) {
	fake := &fakeGoogleOAuth{
		identity: &oauth.Identity{Subject: "s", Email: "alice@x.com"},
	}
	base, _, st := oauthServer(t, fake, nil)

	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, _ := c.Get(base + "/v1/auth/google/login")
	loginResp.Body.Close()
	var sc *http.Cookie
	for _, cc := range loginResp.Cookies() {
		if cc.Name == stateCookieName {
			sc = cc
		}
	}
	state := strings.SplitN(sc.Value, ":", 2)[0]
	req, _ := http.NewRequest(http.MethodGet,
		base+"/v1/auth/google/callback?code=x&state="+state, nil)
	req.AddCookie(sc)
	cbResp, _ := c.Do(req)
	cbResp.Body.Close()
	var sess *http.Cookie
	for _, cc := range cbResp.Cookies() {
		if cc.Name == sessionCookieName {
			sess = cc
		}
	}

	// Logout
	req, _ = http.NewRequest(http.MethodPost, base+"/v1/auth/logout", nil)
	req.AddCookie(sess)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}

	// Session token no longer looks up
	if _, err := st.LookupToken(context.Background(), sess.Value); err == nil {
		t.Fatal("token should be revoked")
	}
}

func TestIsSafeRedirect(t *testing.T) {
	cases := map[string]bool{
		"":                  false,
		"/":                 true,
		"/entries/T-X":      true,
		"//evil.com":        false,
		"https://evil.com":  false,
		"http://evil.com":   false,
		"javascript:alert":  false,
		"//":                false,
		"relative/path":     false,
	}
	for path, want := range cases {
		if got := isSafeRedirect(path); got != want {
			t.Errorf("%q: got %v want %v", path, got, want)
		}
	}
}

func TestCanonicalHostFromBase(t *testing.T) {
	cases := map[string]string{
		"":                            "",
		"http://localhost:8095":       "localhost:8095",
		"https://kb.example.com":      "kb.example.com",
		"http://localhost:8095/":      "localhost:8095",
		"http://localhost:8095/path":  "localhost:8095",
	}
	for in, want := range cases {
		if got := canonicalHostFromBase(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestAuthGoogleLoginCanonicalHostRedirect(t *testing.T) {
	// Use the existing oauthServer helper but set OAuthRedirectBase
	// to a different host than the test server's. Hit /login with Host
	// set to a non-canonical hostname → expect 302 to the canonical
	// host (NO state cookie set yet).
	fake := &fakeGoogleOAuth{
		identity: &oauth.Identity{Subject: "s", Email: "a@x.com", Name: "A"},
	}
	base, _, st := oauthServer(t, fake, nil)
	_ = st
	// Promote the existing Handler to have a canonical host that DIFFERS
	// from the httptest URL. We rebuild a custom router for this test.
	canonical := "http://canonical.example.com:9999"
	_ = canonical
	// Easiest: hit /v1/auth/google/login with a forced Host header that
	// differs from the configured redirect base. oauthServer doesn't
	// set OAuthRedirectBase, so this also covers the "no canonical
	// configured → no redirect" path. To test the redirect path, we
	// need a server with OAuthRedirectBase set. Build it inline.
	st2, srv := canonicalRedirectFixture(t, "http://canonical.example.com:9999")
	_ = st2
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/auth/google/login?next=/x", nil)
	req.Host = "127.0.0.1:8095" // pretend the user accessed via a different host
	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "canonical.example.com:9999") {
		t.Fatalf("location: %s", loc)
	}
	if !strings.Contains(loc, "next=/x") {
		t.Fatalf("next preserved: %s", loc)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == stateCookieName {
			t.Fatalf("state cookie should NOT be set on canonical redirect: %+v", ck)
		}
	}
	// Also ensure the public smoke at /v1/auth/google/login (base — i.e., httptest URL)
	// works because base's URL == its own Host. _ = base avoids lint.
	_ = base
}

// canonicalRedirectFixture is a Handler with a custom OAuthRedirectBase
// for the canonical-host redirect tests.
func canonicalRedirectFixture(t *testing.T, redirectBase string) (*store.Store, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreateUser(context.Background(), &store.User{ID: "admin", Name: "admin", Role: "admin"})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store: st, Enricher: enrich.New("", "", "", "", logger),
		SecretsMode:       config.SecretsOff, Logger: logger,
		OAuthRedirectBase: redirectBase,
	}
	h.OAuthGoogle = &fakeGoogleOAuth{
		identity: &oauth.Identity{Subject: "s", Email: "a@x.com", Name: "A"},
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Audit(st, logger))
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return st, srv
}

func TestAppendTokenQuery(t *testing.T) {
	if got := appendTokenQuery("/foo", "tok"); !strings.Contains(got, "token=tok") {
		t.Fatalf("simple: %s", got)
	}
	// Existing token gets replaced
	if got := appendTokenQuery("/foo?token=old&x=1", "new"); !strings.Contains(got, "token=new") || strings.Contains(got, "token=old") {
		t.Fatalf("replace: %s", got)
	}
}
