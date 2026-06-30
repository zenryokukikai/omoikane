package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func issueToken(t *testing.T, s *store.Store, scopes []string) string {
	t.Helper()
	if err := s.CreateUser(context.Background(),
		&store.User{ID: "u", Name: "u", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	plain, err := s.CreateToken(context.Background(), "u", "t", scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

// chain wraps a handler with the supplied middleware in order.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func TestFromContextEmpty(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestAuthenticateMissingToken(t *testing.T) {
	s := newStore(t)
	mw := &Middleware{S: s}
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	srv := httptest.NewServer(chain(final, mw.Authenticate))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	gotCode := env["error"].(map[string]any)["code"]
	if gotCode != "MISSING_TOKEN" {
		t.Fatalf("code=%v", gotCode)
	}
}

func TestAuthenticateMalformedHeader(t *testing.T) {
	s := newStore(t)
	mw := &Middleware{S: s}
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), mw.Authenticate))
	defer srv.Close()

	for _, h := range []string{"Basic abc", "Bearer", "Bearer   "} {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("Authorization", h)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%q → status=%d", h, resp.StatusCode)
		}
	}
}

func TestAuthenticateBadToken(t *testing.T) {
	s := newStore(t)
	mw := &Middleware{S: s}
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), mw.Authenticate))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer garbage")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestAuthenticateValidToken(t *testing.T) {
	s := newStore(t)
	plain := issueToken(t, s, []string{"read"})
	mw := &Middleware{S: s}

	var seen *store.APIToken
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
		w.WriteHeader(200)
	}), mw.Authenticate))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if seen == nil || seen.Name != "t" {
		t.Fatalf("token not propagated: %+v", seen)
	}
}

func TestAuthenticateBackendError(t *testing.T) {
	s := newStore(t)
	_ = s.Close() // force every subsequent query to fail
	mw := &Middleware{S: s}
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), mw.Authenticate))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env["error"].(map[string]any)["code"] != "INTERNAL" {
		t.Fatalf("expected INTERNAL code, got %+v", env)
	}
}

func TestRequireScopeMissingToken(t *testing.T) {
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), RequireScope("write")))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRequireScopeForbidden(t *testing.T) {
	s := newStore(t)
	plain := issueToken(t, s, []string{"read"})
	mw := &Middleware{S: s}

	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}), mw.Authenticate, RequireScope("write")))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env["error"].(map[string]any)["code"] != "FORBIDDEN" {
		t.Fatalf("code=%v", env)
	}
}

func TestRequireScopeAllowsAdminWildcard(t *testing.T) {
	s := newStore(t)
	plain := issueToken(t, s, []string{"admin"})
	mw := &Middleware{S: s}

	called := false
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	}), mw.Authenticate, RequireScope("write")))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !called {
		t.Fatal("handler not reached")
	}
}

func TestAllowQueryTokenForGETPromotesToHeader(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}), AllowQueryTokenForGET))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?token=hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawAuth != "Bearer hello" {
		t.Fatalf("Authorization=%q", sawAuth)
	}
}

func TestAllowQueryTokenIgnoredOnNonGET(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}), AllowQueryTokenForGET))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/?token=hello", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawAuth != "" {
		t.Fatalf("Authorization should be empty on POST, got %q", sawAuth)
	}
}

func TestExtractBearerDirect(t *testing.T) {
	// Direct unit tests guarantee every branch in extractBearer is hit
	// regardless of how net/http normalises whitespace in headers.
	cases := []struct {
		header  string
		want    string
		wantErr string
	}{
		{"", "", "missing Authorization header"},
		{"Basic abc", "", "malformed Authorization header"},
		{"Bearer", "", "malformed Authorization header"},
		{"Bearer   ", "", "empty token"},
		{"Bearer xyz", "xyz", ""},
	}
	for _, c := range cases {
		r, _ := http.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		got, err := extractBearer(r)
		if c.wantErr == "" {
			if err != nil || got != c.want {
				t.Errorf("%q: got %q, err=%v", c.header, got, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%q: want error %q, got %v", c.header, c.wantErr, err)
		}
	}
}

func TestAllowQueryTokenLeavesExplicitHeaderAlone(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}), AllowQueryTokenForGET))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/?token=ignored", nil)
	req.Header.Set("Authorization", "Bearer explicit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sawAuth != "Bearer explicit" {
		t.Fatalf("expected to keep explicit header, got %q", sawAuth)
	}
}
