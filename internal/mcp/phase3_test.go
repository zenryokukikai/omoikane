package mcp

import (
	"net/http"
	"strings"
	"testing"
)

func TestToolsListIncludesPhase3(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	for _, want := range []string{
		"kb_lookup_by_situation", "kb_feedback", "kb_link", "kb_relations",
	} {
		if !strings.Contains(resp, want) {
			t.Fatalf("tools/list missing %s: %s", want, resp)
		}
	}
}

func TestKBLookupBySituationProxy(t *testing.T) {
	var path string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"matches":[]}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_lookup_by_situation","arguments":{"situation_description":"x"}}}`)
	if path != "/v1/lookup/by-situation" {
		t.Fatalf("path=%s", path)
	}
}

func TestKBFeedbackCreate(t *testing.T) {
	var path, method string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		method = r.Method
		_, _ = w.Write([]byte(`{"CaseID":"CASE-1"}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_feedback","arguments":{"entry_id":"T-X","outcome":"applied"}}}`)
	if path != "/v1/cases" || method != "POST" {
		t.Fatalf("path=%s method=%s", path, method)
	}
}

func TestKBFeedbackUpdate(t *testing.T) {
	var path, method string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		method = r.Method
		_, _ = w.Write([]byte(`{}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_feedback","arguments":{"case_id":"CASE-1","result":"helpful"}}}`)
	if path != "/v1/cases/CASE-1" || method != "PATCH" {
		t.Fatalf("path=%s method=%s", path, method)
	}
}

func TestKBLink(t *testing.T) {
	var path string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_link","arguments":{"from_id":"a","to_id":"b","rel_type":"related"}}}`)
	if path != "/v1/relations" {
		t.Fatalf("path=%s", path)
	}
}

func TestKBRelations(t *testing.T) {
	var path, query string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"relations":[]}`))
	})
	runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_relations","arguments":{"entry_id":"T-X","direction":"both"}}}`)
	if path != "/v1/entries/T-X/relations" || query != "direction=both" {
		t.Fatalf("path=%s query=%s", path, query)
	}
}

func TestKBRelationsMissingEntryID(t *testing.T) {
	s, _ := fixture(t, func(http.ResponseWriter, *http.Request) {})
	resp := runRPC(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_relations","arguments":{}}}`)
	if !strings.Contains(resp, "entry_id required") {
		t.Fatalf("resp: %s", resp)
	}
}
