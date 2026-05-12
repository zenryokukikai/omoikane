package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testHarness sets up a temp HOME, a temp config dir resolver, and a
// configured CLI Config pointed at the given URL/token.
func testHarness(t *testing.T) (cfgPath string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "cli.json")
	SetConfigPath(func() (string, error) { return cfgPath, nil })
	t.Cleanup(func() { SetConfigPath(nil) })
	// Clear env overrides so tests are hermetic.
	t.Setenv("KB_URL", "")
	t.Setenv("KB_TOKEN", "")
	return cfgPath
}

func writeCfg(t *testing.T, cfgPath, url, token string) {
	t.Helper()
	b, _ := json.MarshalIndent(&Config{URL: url, Token: token}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubServer creates an httptest.Server with a custom handler used to
// verify CLI request shapes and inject responses.
func stubServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// ---- Run dispatch ----

func TestRunNoArgsShowsUsage(t *testing.T) {
	out := &bytes.Buffer{}
	if code := Run(nil, nil, out, &bytes.Buffer{}); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("usage")) {
		t.Fatalf("output: %s", out.String())
	}
}

func TestRunVersion(t *testing.T) {
	for _, f := range []string{"version", "-v", "--version"} {
		out := &bytes.Buffer{}
		if code := Run([]string{f}, nil, out, &bytes.Buffer{}); code != 0 {
			t.Fatalf("%s: code=%d", f, code)
		}
		if !bytes.Contains(out.Bytes(), []byte("kb")) {
			t.Fatalf("%s output: %s", f, out.String())
		}
	}
}

func TestRunHelp(t *testing.T) {
	for _, f := range []string{"help", "-h", "--help"} {
		out := &bytes.Buffer{}
		if code := Run([]string{f}, nil, out, &bytes.Buffer{}); code != 0 {
			t.Fatalf("%s: code=%d", f, code)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	stderr := &bytes.Buffer{}
	if code := Run([]string{"bogus"}, nil, &bytes.Buffer{}, stderr); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestRunDispatchesEveryCommand(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/projects" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p","name":"P"}]}`))
		case r.URL.Path == "/v1/projects" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"p","name":"P"}`))
		case r.URL.Path == "/v1/entries" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"id":"T-X","version":1}`))
		case r.URL.Path == "/v1/entries/T-X" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"id":"T-X","title":"x"}`))
		case r.URL.Path == "/v1/entries/T-X" && r.Method == "PATCH":
			_, _ = w.Write([]byte(`{"id":"T-X","version":2}`))
		case r.URL.Path == "/v1/entries/T-X" && r.Method == "DELETE":
			w.WriteHeader(204)
		case r.URL.Path == "/v1/entries/T-X/history" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"history":[{"version":1,"changed_at":"t","status":"DRAFT","change_summary":"init"}]}`))
		case r.URL.Path == "/v1/entries" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"entries":[{"id":"T-X","type":"trap","status":"DRAFT","version":1,"title":"x"}],"pagination":{"limit":50,"offset":0,"total":1,"has_more":false}}`))
		case r.URL.Path == "/v1/search" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"results":[{"entry":{"id":"T-X","type":"trap","title":"x"},"score":0.5}],"count":1,"total":3}`))
		default:
			http.NotFound(w, r)
		}
	})
	writeCfg(t, cfgPath, srv.URL, "tok")

	body := filepath.Join(t.TempDir(), "b.md")
	_ = os.WriteFile(body, []byte("body"), 0o644)

	matrix := []struct {
		name string
		args []string
	}{
		{"projects-list", []string{"projects", "list"}},
		{"projects-create", []string{"projects", "create", "--id", "p", "--name", "P"}},
		{"post", []string{"post", "--project", "p", "--type", "trap", "--title", "T", "--file", body}},
		{"get", []string{"get", "T-X"}},
		{"update", []string{"update", "T-X", "--expected-version", "1", "--status", "ACTIVE"}},
		{"delete", []string{"delete", "T-X"}},
		{"history", []string{"history", "T-X"}},
		{"list", []string{"list", "--project", "p"}},
		{"search", []string{"search", "x"}},
	}
	for _, c := range matrix {
		out := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if code := Run(c.args, nil, out, stderr); code != 0 {
			t.Errorf("%s: code=%d stderr=%s", c.name, code, stderr.String())
		}
	}
}

func TestRunDispatchesConfig(t *testing.T) {
	testHarness(t)
	out := &bytes.Buffer{}
	code := Run([]string{"config", "show"}, nil, out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
}

func TestCmdGetBadFlagAfterPositional(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdGet([]string{"T-X", "--nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

func TestCmdProjectsUnknownSubcommandWithValidClient(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdProjects([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand error")
	}
}

func TestUserHomeDirError(t *testing.T) {
	orig := userHomeDirFn
	t.Cleanup(func() { userHomeDirFn = orig })
	userHomeDirFn = func() (string, error) { return "", io.ErrUnexpectedEOF }
	if _, err := defaultConfigPath(); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunCommandErrorBecomesExit1(t *testing.T) {
	testHarness(t) // no URL set
	stderr := &bytes.Buffer{}
	code := Run([]string{"projects", "list"}, nil, &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("kb:")) {
		t.Fatalf("stderr: %s", stderr.String())
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

// ---- CmdConfig ----

func TestCmdConfigShow(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "u", "t")
	out := &bytes.Buffer{}
	if err := CmdConfig([]string{"show"}, out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"url"`)) {
		t.Fatalf("output: %s", out.String())
	}
}

func TestCmdConfigSetURLAndToken(t *testing.T) {
	cfgPath := testHarness(t)
	if err := CmdConfig([]string{"set", "url", "http://x"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := CmdConfig([]string{"set", "token", "tok"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(b), "http://x") || !strings.Contains(string(b), "tok") {
		t.Fatalf("config: %s", b)
	}
}

func TestCmdConfigErrors(t *testing.T) {
	testHarness(t)
	cases := [][]string{
		{},                       // usage
		{"unknown"},              // unknown verb
		{"set"},                  // wrong arity
		{"set", "weird", "value"}, // unknown key
	}
	for _, c := range cases {
		if err := CmdConfig(c, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: expected error", c)
		}
	}
}

func TestCmdConfigShowLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdConfig([]string{"show"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected load error")
	}
}

func TestCmdConfigSetLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdConfig([]string{"set", "url", "x"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected load error")
	}
}

// ---- CmdProjects ----

func TestCmdProjectsErrors(t *testing.T) {
	testHarness(t)
	if err := CmdProjects(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
	if err := CmdProjects([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdProjectsLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdProjects([]string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdProjectsNoConfig(t *testing.T) {
	testHarness(t)
	if err := CmdProjects([]string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected NewClient error")
	}
}

func TestCmdProjectsCreateMissingFlags(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdProjects([]string{"create"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing-flag error")
	}
}

func TestCmdProjectsCreateWithDesc(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"description":"d"`) {
			t.Errorf("desc not sent: %s", body)
		}
		_, _ = w.Write([]byte(`{"id":"p"}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdProjects([]string{"create", "--id", "p", "--name", "P", "--desc", "d"},
		&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdProjectsListHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdProjects([]string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdProjectsCreateHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	err := CmdProjects([]string{"create", "--id", "p", "--name", "n"},
		&bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdProjectsBadFlags(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdProjects([]string{"create", "--nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

// ---- CmdPost ----

func TestCmdPostRequiresFlags(t *testing.T) {
	testHarness(t)
	if err := CmdPost([]string{}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing-flag error")
	}
}

func TestCmdPostBadFlag(t *testing.T) {
	testHarness(t)
	if err := CmdPost([]string{"--nope"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCmdPostMissingFile(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	err := CmdPost([]string{
		"--project", "p", "--type", "trap", "--title", "T",
		"--file", "/no/such/file.md",
	}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected file-read error")
	}
}

func TestCmdPostFromStdin(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"body":"hello"`) {
			t.Errorf("body not from stdin: %s", raw)
		}
		if !strings.Contains(string(raw), `"status":"DRAFT"`) {
			t.Errorf("status missing: %s", raw)
		}
		if !strings.Contains(string(raw), `"tags":["a","b"]`) {
			t.Errorf("tags missing: %s", raw)
		}
		_, _ = w.Write([]byte(`{"id":"T-X"}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	stdin := strings.NewReader("hello")
	err := CmdPost([]string{
		"--project", "p", "--type", "trap", "--title", "T",
		"--file", "-", "--status", "DRAFT", "--tags", "a,b",
	}, stdin, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCmdPostHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "b.md")
	_ = os.WriteFile(bodyFile, []byte("x"), 0o644)
	err := CmdPost([]string{
		"--project", "p", "--type", "trap", "--title", "T", "--file", bodyFile,
	}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdPostNewClientError(t *testing.T) {
	testHarness(t)
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "b.md")
	_ = os.WriteFile(bodyFile, []byte("x"), 0o644)
	err := CmdPost([]string{
		"--project", "p", "--type", "trap", "--title", "T", "--file", bodyFile,
	}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected NewClient error")
	}
}

func TestCmdPostLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "b.md")
	_ = os.WriteFile(bodyFile, []byte("x"), 0o644)
	err := CmdPost([]string{
		"--project", "p", "--type", "trap", "--title", "T", "--file", bodyFile,
	}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected load error")
	}
}

// ---- CmdGet ----

func TestCmdGetBadArgs(t *testing.T) {
	testHarness(t)
	if err := CmdGet([]string{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
	if err := CmdGet([]string{"--nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag error")
	}
}

func TestCmdGetWithAsOf(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "as_of=") {
			t.Errorf("missing as_of: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"id":"T-X"}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	err := CmdGet([]string{"T-X", "--as-of", "2026-01-01T00:00:00Z"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCmdGetLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdGet([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdGetNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdGet([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdGetHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdGet([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---- CmdUpdate ----

func TestCmdUpdateBadArgs(t *testing.T) {
	testHarness(t)
	if err := CmdUpdate(nil, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
	// leading-dash positional
	if err := CmdUpdate([]string{"-x"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
	// flag parse error
	if err := CmdUpdate([]string{"T-X", "--nope"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
	// no expected-version
	if err := CmdUpdate([]string{"T-X"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdUpdateSuccess(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != "3" {
			t.Errorf("If-Match: %q", r.Header.Get("If-Match"))
		}
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		for _, want := range []string{`"status":"ACTIVE"`, `"title":"T"`,
			`"body":"bbb"`, `"tags":["x","y"]`, `"change_summary":"why"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s in %s", want, s)
			}
		}
		_, _ = w.Write([]byte(`{"id":"T-X","version":4}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	dir := t.TempDir()
	bf := filepath.Join(dir, "b.md")
	_ = os.WriteFile(bf, []byte("bbb"), 0o644)
	err := CmdUpdate([]string{
		"T-X", "--expected-version", "3",
		"--status", "ACTIVE", "--title", "T",
		"--file", bf, "--tags", "x,y", "--summary", "why",
	}, nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCmdUpdateFileError(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	err := CmdUpdate([]string{
		"T-X", "--expected-version", "1", "--file", "/no/such/path",
	}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected file error")
	}
}

func TestCmdUpdateLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	err := CmdUpdate([]string{"T-X", "--expected-version", "1"},
		nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdUpdateNewClientError(t *testing.T) {
	testHarness(t)
	err := CmdUpdate([]string{"T-X", "--expected-version", "1"},
		nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdUpdateHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(409) })
	writeCfg(t, cfgPath, srv.URL, "t")
	err := CmdUpdate([]string{"T-X", "--expected-version", "1", "--status", "ACTIVE"},
		nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- CmdDelete / CmdHistory ----

func TestCmdDeleteBadArgs(t *testing.T) {
	testHarness(t)
	if err := CmdDelete(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdDeleteLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdDelete([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdDeleteNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdDelete([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdDeleteHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdDelete([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdHistoryBadArgs(t *testing.T) {
	testHarness(t)
	if err := CmdHistory(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdHistoryLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdHistory([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdHistoryNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdHistory([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdHistoryHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdHistory([]string{"T-X"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---- CmdList ----

func TestCmdListAllFilters(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, want := range []string{"project_id", "type", "status", "tag",
			"include_superseded", "limit", "offset"} {
			if q.Get(want) == "" {
				t.Errorf("missing query: %s", want)
			}
		}
		_, _ = w.Write([]byte(`{"entries":[{"id":"T","type":"trap","status":"DRAFT","version":1,"title":"x"}],"pagination":{"limit":2,"offset":0,"total":3,"has_more":true}}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := CmdList([]string{
		"--project", "p", "--type", "trap", "--status", "DRAFT",
		"--tag", "x", "--limit", "2", "--offset", "0",
		"--include-superseded",
	}, stdout, stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "page:") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestCmdListBadFlag(t *testing.T) {
	testHarness(t)
	if err := CmdList([]string{"--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdListLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdList(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdListNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdList(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdListHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdList(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdListNoPagination(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// No pagination in response — exercises the `if out.Pagination != nil` skip path.
		_, _ = w.Write([]byte(`{"entries":[]}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	stderr := &bytes.Buffer{}
	if err := CmdList(nil, &bytes.Buffer{}, stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "page:") {
		t.Fatalf("did not expect page line for empty pagination")
	}
}

// ---- CmdSearch ----

func TestCmdSearchBadArgs(t *testing.T) {
	testHarness(t)
	if err := CmdSearch(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdSearchBadFlag(t *testing.T) {
	testHarness(t)
	if err := CmdSearch([]string{"q", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdSearchLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdSearch([]string{"q"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdSearchNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdSearch([]string{"q"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdSearchAllFiltersAndTotal(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		for _, want := range []string{`"project":"p"`, `"type":"trap"`, `"top_k":7`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s in %s", want, s)
			}
		}
		_, _ = w.Write([]byte(`{"results":[{"entry":{"id":"T","type":"trap","title":"x"},"score":0.5}],"total":5}`))
	})
	writeCfg(t, cfgPath, srv.URL, "t")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := CmdSearch([]string{"hello world", "--project", "p", "--type", "trap", "--top-k", "7"},
		stdout, stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "of 5 total") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestCmdSearchHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := stubServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	writeCfg(t, cfgPath, srv.URL, "t")
	if err := CmdSearch([]string{"q"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---- helpers ----

func TestPrepareFTSQuery(t *testing.T) {
	if PrepareFTSQuery("") != "" {
		t.Fatal("empty input should pass through")
	}
	if PrepareFTSQuery(" ") != " " {
		t.Fatal("whitespace-only should pass through unchanged")
	}
	got := PrepareFTSQuery(`mask "tricky`)
	want := `"mask"* "tricky"*`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSplitCSV(t *testing.T) {
	got := SplitCSV("a, , b ,c,")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	_ = os.WriteFile(p, []byte("data"), 0o644)
	b, err := ReadFile(p, nil)
	if err != nil || string(b) != "data" {
		t.Fatalf("file: %q err=%v", b, err)
	}
	b, err = ReadFile("-", strings.NewReader("stdin"))
	if err != nil || string(b) != "stdin" {
		t.Fatalf("stdin: %q err=%v", b, err)
	}
}

func TestUsage(t *testing.T) {
	buf := &bytes.Buffer{}
	Usage(buf)
	if !strings.Contains(buf.String(), "omoikane CLI") {
		t.Fatalf("usage: %s", buf.String())
	}
}

func TestHTTPClientFn(t *testing.T) {
	// Smoke test of the default client factory: no panic, returns non-nil.
	c := httpClientFn()
	if c == nil || c.Timeout == 0 {
		t.Fatalf("client: %+v", c)
	}
}

// ensure unused imports are kept (context/time/json used elsewhere)
var (
	_ = context.Background
	_ = time.Now
	_ = json.Unmarshal
)
