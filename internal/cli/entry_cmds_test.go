package cli

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestCmdGetBadFlagAfterPositional(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdGet([]string{"T-X", "--nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

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
