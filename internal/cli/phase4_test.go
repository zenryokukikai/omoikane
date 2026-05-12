package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func phase4Stub(t *testing.T) string {
	t.Helper()
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/browse" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"nodes":[{"ID":"H-1","Name":"root"}]}`))
		case r.URL.Path == "/v1/browse" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"ID":"H-1","Name":"root"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/browse/H-1/entries"):
			if r.Method == "POST" {
				w.WriteHeader(204)
			} else if r.Method == "DELETE" {
				w.WriteHeader(204)
			} else {
				_, _ = w.Write([]byte(`{"entries":[]}`))
			}
		case strings.HasPrefix(r.URL.Path, "/v1/browse/H-1"):
			if r.Method == "DELETE" {
				w.WriteHeader(204)
			} else {
				_, _ = w.Write([]byte(`{"node":{"ID":"H-1"}}`))
			}
		case r.URL.Path == "/v1/index":
			_, _ = w.Write([]byte(`{"group_by":"tag","buckets":[{"Key":"mask","Label":"mask","Count":3}]}`))
		case r.URL.Path == "/v1/reflect":
			_, _ = w.Write([]byte(`{"engine":"heuristic","summary":"x","entries":[]}`))
		default:
			http.NotFound(w, r)
		}
	})
	return srv.URL
}

func TestCmdBrowse(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, phase4Stub(t), "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"list", []string{"list", "--project", "p"}},
		{"create", []string{"create", "--name", "root", "--parent", "x", "--project", "p", "--description", "d"}},
		{"get", []string{"get", "H-1"}},
		{"attach", []string{"attach", "--node", "H-1", "--entry", "T-X", "--weight", "0.5"}},
		{"detach", []string{"detach", "--node", "H-1", "--entry", "T-X"}},
		{"delete", []string{"delete", "H-1"}},
	} {
		if err := CmdBrowse(c.args, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestCmdBrowseValidation(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	if err := CmdBrowse(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage err")
	}
	if err := CmdBrowse([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand")
	}
	for _, args := range [][]string{
		{"create"},
		{"get"},
		{"attach"},
		{"detach"},
		{"delete"},
	} {
		if err := CmdBrowse(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected validation err: %v", args)
		}
	}
}

func TestCmdIndexAndReflect(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, phase4Stub(t), "tok")
	if err := CmdIndex([]string{"--group-by", "tag", "--project", "p"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := CmdReflect([]string{"T-X", "T-Y", "--prompt", "compare"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("reflect: %v", err)
	}
}

func TestCmdReflectMissingArgs(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	if err := CmdReflect([]string{"--prompt", "x"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected entry-id error")
	}
}

func TestRunPhase4Dispatch(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, phase4Stub(t), "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"browse", []string{"browse", "list"}},
		{"index", []string{"index"}},
		{"reflect", []string{"reflect", "T-X"}},
	} {
		if code := Run(c.args, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
			t.Fatalf("%s: code=%d", c.name, code)
		}
	}
}
