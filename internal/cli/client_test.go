package cli

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestUserHomeDirError(t *testing.T) {
	orig := userHomeDirFn
	t.Cleanup(func() { userHomeDirFn = orig })
	userHomeDirFn = func() (string, error) { return "", io.ErrUnexpectedEOF }
	if _, err := defaultConfigPath(); err == nil {
		t.Fatal("expected error")
	}
}

// ---- config persistence ----

func TestSetConfigPathNilRestoresDefault(t *testing.T) {
	SetConfigPath(func() (string, error) { return "/tmp/something", nil })
	SetConfigPath(nil)
	p, err := configPathFn()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "/.config/omoikane/cli.json") {
		t.Fatalf("default not restored: %s", p)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	testHarness(t)
	t.Setenv("KB_URL", "https://env-url")
	t.Setenv("KB_TOKEN", "env-token")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://env-url" || c.Token != "env-token" {
		t.Fatalf("env not applied: %+v", c)
	}
}

func TestLoadConfigPathError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadReadFileError(t *testing.T) {
	// Point at a directory rather than a file → ReadFile fails with a
	// non-NotExist error.
	dir := t.TempDir()
	SetConfigPath(func() (string, error) { return dir, nil })
	t.Cleanup(func() { SetConfigPath(nil) })
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBadJSON(t *testing.T) {
	cfgPath := testHarness(t)
	_ = os.WriteFile(cfgPath, []byte("{not json"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestSaveCreatesDir(t *testing.T) {
	cfgPath := testHarness(t)
	if err := Save(&Config{URL: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"url"`) {
		t.Fatalf("saved: %s", b)
	}
}

func TestSaveConfigPathError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := Save(&Config{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveMkdirError(t *testing.T) {
	// Point at a path whose parent cannot be created.
	SetConfigPath(func() (string, error) { return "/dev/null/cli.json", nil })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := Save(&Config{}); err == nil {
		t.Fatal("expected mkdir error")
	}
}

// ---- NewClient ----

func TestNewClientMissingURL(t *testing.T) {
	if _, err := NewClient(&Config{Token: "x"}); err == nil {
		t.Fatal("expected error")
	}
}
func TestNewClientMissingToken(t *testing.T) {
	if _, err := NewClient(&Config{URL: "x"}); err == nil {
		t.Fatal("expected error")
	}
}
func TestNewClientStripsTrailingSlash(t *testing.T) {
	c, err := NewClient(&Config{URL: "http://x/", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://x" {
		t.Fatalf("URL=%s", c.URL)
	}
}

// ---- Client.Do ----

func TestDoSerializesAndDecodes(t *testing.T) {
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Client-Type") != "cli" {
			t.Errorf("client type: %q", r.Header.Get("X-Client-Type"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type: %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Extra") != "yes" {
			t.Errorf("extra header missing")
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"k":"v"`) {
			t.Errorf("body: %s", raw)
		}
		_, _ = w.Write([]byte(`{"echo":1}`))
	})
	c, _ := NewClient(&Config{URL: srv.URL, Token: "t"})
	var out map[string]any
	if err := c.Do(http.MethodPost, "/p", map[string]string{"k": "v"},
		map[string]string{"X-Extra": "yes"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["echo"] != float64(1) {
		t.Fatalf("echo: %v", out["echo"])
	}
}

func TestDoNoBodyOrInto(t *testing.T) {
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "" {
			t.Errorf("content-type should not be set: %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(204)
	})
	c, _ := NewClient(&Config{URL: srv.URL, Token: "t"})
	if err := c.Do(http.MethodDelete, "/p", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDoErrorStatus(t *testing.T) {
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"oops"}`))
	})
	c, _ := NewClient(&Config{URL: srv.URL, Token: "t"})
	err := c.Do(http.MethodGet, "/", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err=%v", err)
	}
}

func TestDoBadJSONResponse(t *testing.T) {
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	})
	c, _ := NewClient(&Config{URL: srv.URL, Token: "t"})
	var out map[string]any
	if err := c.Do(http.MethodGet, "/", nil, nil, &out); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestDoRequestError(t *testing.T) {
	// Network error: client points at unreachable address.
	c, _ := NewClient(&Config{URL: "http://127.0.0.1:1", Token: "t"})
	if err := c.Do(http.MethodGet, "/", nil, nil, nil); err == nil {
		t.Fatal("expected network error")
	}
}

func TestDoMarshalError(t *testing.T) {
	c, _ := NewClient(&Config{URL: "http://x", Token: "t"})
	// chan can't be marshaled
	if err := c.Do(http.MethodPost, "/", make(chan int), nil, nil); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestDoNewRequestError(t *testing.T) {
	// Invalid method character triggers http.NewRequest failure.
	c, _ := NewClient(&Config{URL: "http://x", Token: "t"})
	if err := c.Do("BAD\nMETHOD", "/", nil, nil, nil); err == nil {
		t.Fatal("expected NewRequest error")
	}
}

func TestHTTPClientFn(t *testing.T) {
	// Smoke test of the default client factory: no panic, returns non-nil.
	c := httpClientFn()
	if c == nil || c.Timeout == 0 {
		t.Fatalf("client: %+v", c)
	}
}
