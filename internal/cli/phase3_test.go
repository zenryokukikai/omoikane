package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// phase3Stub returns a stub HTTP server that emits canned responses for every
// Phase 3 endpoint the CLI hits. It also remembers the last call for spot
// checks.
func phase3Stub(t *testing.T) (url string, lastPath *string) {
	t.Helper()
	var last string
	srv := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		last = r.Method + " " + r.URL.Path
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/cases"):
			switch r.Method {
			case "POST":
				_, _ = w.Write([]byte(`{"CaseID":"CASE-1"}`))
			case "PATCH":
				_, _ = w.Write([]byte(`{"CaseID":"CASE-1","Result":"helpful"}`))
			case "GET":
				_, _ = w.Write([]byte(`{"CaseID":"CASE-1"}`))
			}
		case r.URL.Path == "/v1/entries/T-X/cases":
			_, _ = w.Write([]byte(`{"cases":[]}`))
		case r.URL.Path == "/v1/entries/T-X/signals":
			_, _ = w.Write([]byte(`{"id":"T-X","total_uses":3}`))
		case r.URL.Path == "/v1/review-queue":
			_, _ = w.Write([]byte(`{"queue":[{"id":"T-Y","title":"y","misleading_count":3,"total_uses":4,"helpfulness_score":-0.5}]}`))
		case r.URL.Path == "/v1/relations" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"FromID":"a","ToID":"b","RelType":"related"}`))
		case r.URL.Path == "/v1/relations" && r.Method == "DELETE":
			w.WriteHeader(204)
		case r.URL.Path == "/v1/entries/T-X/relations":
			_, _ = w.Write([]byte(`{"relations":[{"FromID":"T-X","ToID":"T-Y","RelType":"related","Confidence":0.9}]}`))
		case r.URL.Path == "/v1/situations" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"ID":"SIT-1"}`))
		case r.URL.Path == "/v1/situations" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"situations":[{"ID":"SIT-1","Description":"x"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/situations/SIT-1"):
			if strings.HasSuffix(r.URL.Path, "/entries") && r.Method == "POST" {
				w.WriteHeader(204)
				return
			}
			if r.Method == "DELETE" {
				w.WriteHeader(204)
				return
			}
			_, _ = w.Write([]byte(`{"situation":{"ID":"SIT-1"},"entries":[]}`))
		case r.URL.Path == "/v1/lookup/by-situation":
			_, _ = w.Write([]byte(`{"matches":[{"entry_id":"T-X","score":0.5,"type":"trap","status":"ACTIVE","title":"X"}]}`))
		case r.URL.Path == "/v1/clusters" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"clusters":[{"ID":"CL-1","Status":"OPEN","MemberCount":3,"Title":"T"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/clusters/CL-1/promote"):
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/v1/clusters/CL-1/dismiss"):
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/v1/clusters/CL-1"):
			_, _ = w.Write([]byte(`{"cluster":{"ID":"CL-1"},"members":[]}`))
		case r.URL.Path == "/v1/clusters/rebuild":
			_, _ = w.Write([]byte(`{"clusters_created":1,"members_added":3}`))
		default:
			http.NotFound(w, r)
		}
	})
	return srv.URL, &last
}

func TestCmdFeedbackRecord(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	out := &bytes.Buffer{}
	if err := CmdFeedback([]string{"record", "--entry", "T-X",
		"--trigger", "x", "--outcome", "applied", "--result", "helpful", "--notes", "n"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "CASE-1") {
		t.Fatalf("out: %s", out.String())
	}
}

func TestCmdFeedbackJudgeSignalsReviewQueue(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"judge", []string{"judge", "--case", "CASE-1",
			"--outcome", "applied", "--result", "helpful", "--evidence", "e", "--by", "me", "--notes", "n"}},
		{"signals", []string{"signals", "T-X"}},
		{"review-queue", []string{"review-queue", "--limit", "5"}},
	} {
		out := &bytes.Buffer{}
		if err := CmdFeedback(c.args, out); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestCmdFeedbackUsage(t *testing.T) {
	if err := CmdFeedback(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage error")
	}
	if err := CmdFeedback([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand error")
	}
}

func TestCmdFeedbackMissingFlags(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	// record requires --entry
	if err := CmdFeedback([]string{"record"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected entry-required error")
	}
	// judge requires --case
	if err := CmdFeedback([]string{"judge"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected case-required error")
	}
	// signals requires one arg
	if err := CmdFeedback([]string{"signals"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected signals usage error")
	}
}

func TestCmdRelations(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"link", []string{"link", "--from", "a", "--to", "b", "--type", "related",
			"--confidence", "0.9", "--source", "test", "--notes", "n"}},
		{"unlink", []string{"unlink", "--from", "a", "--to", "b", "--type", "related"}},
		{"list", []string{"list", "--entry", "T-X", "--direction", "both"}},
	} {
		if err := CmdRelations(c.args, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestCmdRelationsValidation(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	if err := CmdRelations(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage err")
	}
	if err := CmdRelations([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand err")
	}
	for _, args := range [][]string{
		{"link"},
		{"unlink"},
		{"list"},
	} {
		if err := CmdRelations(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected missing-flag error: %v", args)
		}
	}
}

func TestCmdSituations(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	cases := []struct {
		name string
		args []string
	}{
		{"create", []string{"create", "--description", "x", "--project", "p", "--domain", "d"}},
		{"list", []string{"list", "--project", "p"}},
		{"get", []string{"get", "SIT-1"}},
		{"link", []string{"link", "--situation", "SIT-1", "--entry", "T-X", "--relevance", "0.9", "--notes", "n"}},
		{"lookup", []string{"lookup", "--query", "x", "--top-k", "5", "--project", "p"}},
		{"delete", []string{"delete", "SIT-1"}},
	}
	for _, c := range cases {
		if err := CmdSituations(c.args, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestCmdSituationsCreateFromFile(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	stdin := strings.NewReader("read from stdin")
	if err := CmdSituations([]string{"create", "--file", "-"}, stdin, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdSituationsValidation(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	if err := CmdSituations(nil, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage err")
	}
	if err := CmdSituations([]string{"weird"}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand")
	}
	for _, args := range [][]string{
		{"create"},
		{"get"},
		{"link"},
		{"delete"},
		{"lookup"},
	} {
		if err := CmdSituations(args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("expected validation err: %v", args)
		}
	}
}

func TestCmdCluster(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"list", []string{"list", "--project", "p", "--status", "OPEN"}},
		{"get", []string{"get", "CL-1"}},
		{"promote", []string{"promote", "--cluster", "CL-1", "--entry", "T-X"}},
		{"dismiss", []string{"dismiss", "CL-1"}},
		{"rebuild", []string{"rebuild", "--threshold", "0.3", "--min-members", "2"}},
	} {
		if err := CmdCluster(c.args, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestCmdClusterValidation(t *testing.T) {
	cfg := testHarness(t)
	writeCfg(t, cfg, "http://x", "t")
	if err := CmdCluster(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("expected usage err")
	}
	if err := CmdCluster([]string{"weird"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown-subcommand")
	}
	for _, args := range [][]string{
		{"get"},
		{"promote"},
		{"dismiss"},
	} {
		if err := CmdCluster(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected validation err: %v", args)
		}
	}
}

// Touch the Run dispatcher's Phase 3 cases so they're covered.
func TestRunPhase3Dispatch(t *testing.T) {
	cfg := testHarness(t)
	u, _ := phase3Stub(t)
	writeCfg(t, cfg, u, "tok")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"feedback", []string{"feedback", "review-queue"}},
		{"relations", []string{"relations", "list", "--entry", "T-X"}},
		{"situations", []string{"situations", "list"}},
		{"cluster", []string{"cluster", "list"}},
	} {
		out := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if code := Run(c.args, nil, out, stderr); code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", c.name, code, stderr.String())
		}
	}
}
