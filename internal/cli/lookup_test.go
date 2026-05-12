package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lookupStub returns a stub Core that records the path + body and returns
// a fixed matches list.
func lookupStub(t *testing.T, expectPath string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != expectPath {
			t.Errorf("path: %s, want %s", r.URL.Path, expectPath)
		}
		_, _ = w.Write([]byte(`{"matches":[
			{"entry_id":"T-1","score":0.9,"type":"trap","status":"ACTIVE","title":"first","prohibited":"DO NOT do X"},
			{"entry_id":"T-2","score":0.5,"type":"decision","status":"ACTIVE","title":"second"}
		]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCmdLookupDispatch(t *testing.T) {
	testHarness(t)
	if err := CmdLookup(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage error")
	}
	if err := CmdLookup([]string{"weird"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown subcommand error")
	}
}

func TestCmdLookupTriggerSuccess(t *testing.T) {
	cfgPath := testHarness(t)
	srv := lookupStub(t, "/v1/lookup/by-trigger")
	writeCfg(t, cfgPath, srv.URL, "tok")

	stdout := &bytes.Buffer{}
	if err := CmdLookup([]string{
		"trigger",
		"--query", "modify mask generation",
		"--domain", "preprocessing",
		"--project", "kb",
		"--top-k", "5",
	}, stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "T-1") || !strings.Contains(out, "PROHIBITED") {
		t.Fatalf("output missing expected lines: %s", out)
	}
}

func TestCmdLookupTriggerMissingQuery(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "t")
	if err := CmdLookup([]string{"trigger"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdLookupTriggerBadFlag(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"trigger", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCmdLookupTriggerLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	t.Cleanup(func() { SetConfigPath(nil) })
	if err := CmdLookup([]string{"trigger", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdLookupTriggerNewClientError(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"trigger", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected NewClient error")
	}
}

func TestCmdLookupTriggerHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	writeCfg(t, cfgPath, srv.URL, "tok")
	if err := CmdLookup([]string{"trigger", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected http error")
	}
}

func TestCmdLookupSymptomSuccess(t *testing.T) {
	cfgPath := testHarness(t)
	srv := lookupStub(t, "/v1/lookup/by-symptom")
	writeCfg(t, cfgPath, srv.URL, "tok")
	if err := CmdLookup([]string{
		"symptom",
		"--query", "rectangular artifact",
		"--project", "kb",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdLookupSymptomBadInputs(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"symptom"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing query error")
	}
	if err := CmdLookup([]string{"symptom", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected parse error")
	}

	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	defer SetConfigPath(nil)
	if err := CmdLookup([]string{"symptom", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected load error")
	}
}

func TestCmdLookupSymptomNewClientAndHTTPErrors(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"symptom", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected NewClient error")
	}
	cfgPath := testHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	writeCfg(t, cfgPath, srv.URL, "tok")
	if err := CmdLookup([]string{"symptom", "--query", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected http error")
	}
}

func TestCmdLookupTagsSuccess(t *testing.T) {
	cfgPath := testHarness(t)
	srv := lookupStub(t, "/v1/lookup/by-tags")
	writeCfg(t, cfgPath, srv.URL, "tok")
	if err := CmdLookup([]string{
		"tags", "--tags", "mask,preprocessing", "--mode", "all",
		"--project", "kb",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdLookupTagsBadInputs(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"tags"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing tags error")
	}
	if err := CmdLookup([]string{"tags", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected parse error")
	}
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	defer SetConfigPath(nil)
	if err := CmdLookup([]string{"tags", "--tags", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected load error")
	}
}

func TestCmdLookupTagsNewClientAndHTTPErrors(t *testing.T) {
	testHarness(t)
	if err := CmdLookup([]string{"tags", "--tags", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected NewClient error")
	}
	cfgPath := testHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	writeCfg(t, cfgPath, srv.URL, "tok")
	if err := CmdLookup([]string{"tags", "--tags", "x"},
		&bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected http error")
	}
}

func TestCmdIncidentSuccess(t *testing.T) {
	cfgPath := testHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/entries" {
			t.Errorf("path=%q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		for _, want := range []string{`"type":"incident"`,
			`"status":"INVESTIGATING"`, `"attempted_approaches"`,
			`"observed_behavior"`, `"hypotheses"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s in %s", want, s)
			}
		}
		_, _ = w.Write([]byte(`{"id":"I-1"}`))
	}))
	defer srv.Close()
	writeCfg(t, cfgPath, srv.URL, "tok")

	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	_ = os.WriteFile(body, []byte("body content"), 0o644)
	att := filepath.Join(dir, "attempted.md")
	_ = os.WriteFile(att, []byte("tried fp16"), 0o644)
	obs := filepath.Join(dir, "observed.md")
	_ = os.WriteFile(obs, []byte("NaN at 5000"), 0o644)
	hyp := filepath.Join(dir, "hypo.md")
	_ = os.WriteFile(hyp, []byte("attention precision"), 0o644)

	stdout := &bytes.Buffer{}
	err := CmdIncident([]string{
		"--project", "kb",
		"--title", "H100 NaN",
		"--file", body,
		"--symptom", "NaN appears",
		"--tags", "ml,h100",
		"--attempted", att,
		"--observed", obs,
		"--hypotheses", hyp,
	}, nil, stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"id": "I-1"`) {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestCmdIncidentMissingFlags(t *testing.T) {
	testHarness(t)
	if err := CmdIncident(nil, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing-flag error")
	}
}

func TestCmdIncidentBadFlag(t *testing.T) {
	testHarness(t)
	if err := CmdIncident([]string{"--nope"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCmdIncidentFileMissing(t *testing.T) {
	cfgPath := testHarness(t)
	writeCfg(t, cfgPath, "http://x", "tok")
	if err := CmdIncident([]string{
		"--project", "p", "--title", "t", "--file", "/no/such/file",
	}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected file error")
	}
}

func TestCmdIncidentAuxFileMissing(t *testing.T) {
	cfgPath := testHarness(t)
	dir := t.TempDir()
	body := filepath.Join(dir, "b.md")
	_ = os.WriteFile(body, []byte("x"), 0o644)
	writeCfg(t, cfgPath, "http://x", "tok")
	if err := CmdIncident([]string{
		"--project", "p", "--title", "t", "--file", body,
		"--attempted", "/no/such/path",
	}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected aux file error")
	}
}

func TestCmdIncidentLoadError(t *testing.T) {
	SetConfigPath(func() (string, error) { return "", io.ErrUnexpectedEOF })
	defer SetConfigPath(nil)
	dir := t.TempDir()
	body := filepath.Join(dir, "b.md")
	_ = os.WriteFile(body, []byte("x"), 0o644)
	if err := CmdIncident([]string{
		"--project", "p", "--title", "t", "--file", body,
	}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected load error")
	}
}

func TestCmdIncidentNewClientError(t *testing.T) {
	testHarness(t)
	dir := t.TempDir()
	body := filepath.Join(dir, "b.md")
	_ = os.WriteFile(body, []byte("x"), 0o644)
	if err := CmdIncident([]string{
		"--project", "p", "--title", "t", "--file", body,
	}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected NewClient error")
	}
}

func TestCmdIncidentHTTPError(t *testing.T) {
	cfgPath := testHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	writeCfg(t, cfgPath, srv.URL, "tok")
	dir := t.TempDir()
	body := filepath.Join(dir, "b.md")
	_ = os.WriteFile(body, []byte("x"), 0o644)
	if err := CmdIncident([]string{
		"--project", "p", "--title", "t", "--file", body,
	}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected http error")
	}
}

func TestRunDispatchesPhase2Commands(t *testing.T) {
	cfgPath := testHarness(t)
	srv := lookupStub(t, "/v1/lookup/by-trigger")
	writeCfg(t, cfgPath, srv.URL, "tok")

	if code := Run([]string{"lookup", "trigger", "--query", "x"},
		nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("lookup dispatch code=%d", code)
	}

	dir := t.TempDir()
	body := filepath.Join(dir, "b.md")
	_ = os.WriteFile(body, []byte("x"), 0o644)
	// incident dispatch
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"I-2"}`))
	}))
	defer stub.Close()
	writeCfg(t, cfgPath, stub.URL, "tok")
	if code := Run([]string{"incident",
		"--project", "p", "--title", "t", "--file", body,
	}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("incident dispatch code=%d", code)
	}
}

// ensure unused-import linter happy
var _ = json.Marshal
