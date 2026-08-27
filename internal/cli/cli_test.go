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

func TestCmdProjectsUnknownSubcommandWithValidClient(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdProjects([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand error")
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
		{},                        // usage
		{"unknown"},               // unknown verb
		{"set"},                   // wrong arity
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

// ---- helpers ----

func TestSplitCSV(t *testing.T) {
	got := SplitCSV("a, , b ,c,")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestUsage(t *testing.T) {
	buf := &bytes.Buffer{}
	Usage(buf)
	if !strings.Contains(buf.String(), "omoikane CLI") {
		t.Fatalf("usage: %s", buf.String())
	}
}

// ensure unused imports are kept (context/time/json used elsewhere)
var (
	_ = context.Background
	_ = time.Now
	_ = json.Unmarshal
)
