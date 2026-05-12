package mcp

import (
	"net/http"
	"strings"
	"testing"
)

func TestToolsListIncludesPhase4(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	for _, want := range []string{"kb_browse", "kb_reflect"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("tools/list missing %s", want)
		}
	}
}

func TestKBBrowseRoots(t *testing.T) {
	var path string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_browse","arguments":{}}}`)
	if path != "/v1/browse" {
		t.Fatalf("path=%s", path)
	}
}

func TestKBBrowseNode(t *testing.T) {
	var path string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_browse","arguments":{"node_id":"H-1"}}}`)
	if path != "/v1/browse/H-1" {
		t.Fatalf("path=%s", path)
	}
}

func TestKBBrowseNodeEntries(t *testing.T) {
	var path string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_browse","arguments":{"node_id":"H-1","include_entries":true}}}`)
	if path != "/v1/browse/H-1/entries" {
		t.Fatalf("path=%s", path)
	}
}

func TestKBReflect(t *testing.T) {
	var path, method string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		method = r.Method
		_, _ = w.Write([]byte(`{}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_reflect","arguments":{"entry_ids":["T-X"]}}}`)
	if path != "/v1/reflect" || method != "POST" {
		t.Fatalf("path=%s method=%s", path, method)
	}
}
