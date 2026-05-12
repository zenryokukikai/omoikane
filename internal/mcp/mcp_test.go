package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixture returns an MCP server pointed at a fake Core API + the recorder
// for incoming requests.
func fixture(t *testing.T, handler http.HandlerFunc) (*Server, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Server{
		CoreURL:   srv.URL,
		Token:     "tok",
		ProjectID: "kb",
	}, srv
}

// runRPC sends a single JSON-RPC message and returns the response.
func runRPC(t *testing.T, s *Server, req string) string {
	t.Helper()
	in := strings.NewReader(req + "\n")
	out := &bytes.Buffer{}
	if err := s.Run(context.Background(), in, out); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func TestInitialize(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if !strings.Contains(resp, `"protocolVersion"`) {
		t.Fatalf("missing protocolVersion: %s", resp)
	}
	if !strings.Contains(resp, "omoikane-kb") {
		t.Fatalf("missing serverInfo name: %s", resp)
	}
}

func TestNotificationsIgnored(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp != "" {
		t.Fatalf("expected no response for notification, got %s", resp)
	}
}

func TestShutdownNotification(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","method":"shutdown"}`)
	if resp != "" {
		t.Fatalf("expected no response for shutdown notification, got %s", resp)
	}
	// With an ID, shutdown returns a response.
	resp = runRPC(t, s, `{"jsonrpc":"2.0","id":2,"method":"shutdown"}`)
	if !strings.Contains(resp, `"result"`) {
		t.Fatalf("expected result: %s", resp)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":3,"method":"bogus"}`)
	if !strings.Contains(resp, `"code":-32601`) {
		t.Fatalf("expected method-not-found, got %s", resp)
	}
}

func TestUnknownMethodNotificationIgnored(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","method":"bogus"}`)
	if resp != "" {
		t.Fatalf("notification should not respond: %s", resp)
	}
}

func TestParseError(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{not json`)
	if !strings.Contains(resp, `-32700`) {
		t.Fatalf("expected parse error: %s", resp)
	}
}

func TestToolsList(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	for _, want := range []string{
		"kb_lookup_by_trigger", "kb_lookup_by_symptom",
		"kb_lookup_by_tags", "kb_search", "kb_get", "kb_post",
	} {
		if !strings.Contains(resp, want) {
			t.Errorf("missing tool %q in list: %s", want, resp)
		}
	}
}

func TestToolsCallLookupByTrigger(t *testing.T) {
	var sawBody string
	var sawAuth string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r.Body)
		sawBody = string(body)
		sawAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/lookup/by-trigger" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"matches":[{"entry_id":"T-1"}]}`))
	})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"kb_lookup_by_trigger","arguments":{"trigger_description":"modify mask generation"}}}`)
	if !strings.Contains(resp, "T-1") {
		t.Fatalf("expected entry_id in response: %s", resp)
	}
	if !strings.Contains(sawBody, "kb") {
		t.Fatalf("project_id default not merged: %s", sawBody)
	}
	if sawAuth != "Bearer tok" {
		t.Fatalf("auth header: %q", sawAuth)
	}
}

func TestToolsCallEachKnownTool(t *testing.T) {
	pathSeen := ""
	methodSeen := ""
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		pathSeen = r.URL.Path
		methodSeen = r.Method
		_, _ = w.Write([]byte(`{}`))
	})
	cases := []struct {
		tool   string
		args   map[string]any
		path   string
		method string
	}{
		{"kb_lookup_by_symptom", map[string]any{"symptom_description": "x"}, "/v1/lookup/by-symptom", "POST"},
		{"kb_lookup_by_tags", map[string]any{"tags": []string{"x"}}, "/v1/lookup/by-tags", "POST"},
		{"kb_search", map[string]any{"query": "x"}, "/v1/search", "POST"},
		{"kb_get", map[string]any{"entry_id": "T-1"}, "/v1/entries/T-1", "GET"},
		{"kb_post", map[string]any{"project_id": "p", "type": "trap"}, "/v1/entries", "POST"},
	}
	for _, c := range cases {
		payload := map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": c.tool, "arguments": c.args},
		}
		b, _ := json.Marshal(payload)
		runRPC(t, s, string(b))
		if pathSeen != c.path {
			t.Errorf("%s: path=%q want %q", c.tool, pathSeen, c.path)
		}
		if methodSeen != c.method {
			t.Errorf("%s: method=%q want %q", c.tool, methodSeen, c.method)
		}
	}
}

func TestToolsCallGetWithAsOf(t *testing.T) {
	gotQuery := ""
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{}`))
	})
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_get","arguments":{"entry_id":"T-1","as_of":"2026-01-01T00:00:00Z"}}}`
	runRPC(t, s, payload)
	if !strings.HasPrefix(gotQuery, "as_of=") {
		t.Fatalf("as_of not passed through: %q", gotQuery)
	}
}

func TestToolsCallGetMissingEntryID(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_get","arguments":{}}}`)
	if !strings.Contains(resp, `entry_id required`) {
		t.Fatalf("expected error: %s", resp)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_unknown","arguments":{}}}`)
	if !strings.Contains(resp, "unknown tool") {
		t.Fatalf("expected unknown-tool error: %s", resp)
	}
}

func TestToolsCallBadParams(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not-an-object"}`)
	if !strings.Contains(resp, `invalid params`) {
		t.Fatalf("expected invalid params: %s", resp)
	}
}

func TestToolsCallCoreError(t *testing.T) {
	s, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"x"}}}`)
	if !strings.Contains(resp, `"isError":true`) {
		t.Fatalf("expected isError flag: %s", resp)
	}
}

func TestToolsCallTransportFailureFailsOpen(t *testing.T) {
	// Point at an unreachable address so the HTTP client errors.
	s := &Server{CoreURL: "http://127.0.0.1:1", Token: "x"}
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"x"}}}`)
	if !strings.Contains(resp, "kb_unavailable") {
		t.Fatalf("expected kb_unavailable flag: %s", resp)
	}
}

func TestMergeProjectNoOverwrite(t *testing.T) {
	got := mergeProject(map[string]any{"project_id": "explicit"}, "default")
	if got["project_id"] != "explicit" {
		t.Fatalf("got %v", got)
	}
}

func TestMergeProjectEmptyDefault(t *testing.T) {
	got := mergeProject(map[string]any{"foo": 1}, "")
	if _, ok := got["project_id"]; ok {
		t.Fatalf("should not add project_id when default empty: %+v", got)
	}
}

func TestRunPropagatesScannerError(t *testing.T) {
	// Feed a JSON line larger than the buffer cap — scanner returns err.
	huge := strings.Repeat("a", 5*1024*1024)
	s := &Server{CoreURL: "http://x", Token: "tok"}
	if err := s.Run(context.Background(), strings.NewReader(huge+"\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected scanner error")
	}
}

// readAll is a tiny re-export of io.ReadAll without pulling the import
// across the test file for one use.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := bytes.Buffer{}
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if buf.Len() > 0 {
				return buf.Bytes(), nil
			}
			return nil, err
		}
	}
}
