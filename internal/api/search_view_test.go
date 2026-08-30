package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// /v1/search response shape (issue #138). A full-entry hit runs into the
// kilobytes, so top_k=5 buries an agent in text it did not ask for; the
// index view returns what a reader needs to choose (title + the matched
// excerpt) and nothing else. The default stays byte-compatible so the
// dashboard and the dist/ librarian scripts keep working untouched.

// seedSearchCorpus creates enough distinctive entries that a top_k=5
// search really returns five hits.
func seedSearchCorpus(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat(
		"Background context that makes each entry realistically long, "+
			"the way a real trap write-up is. ", 12)
	for _, title := range []string{
		"Sidecar restart trap", "Sidecar drain race", "Sidecar log rotation",
		"Sidecar readiness gate", "Sidecar image pin", "Sidecar metrics gap",
	} {
		if _, err := st.CreateEntry(ctx, &store.Entry{
			ProjectID: "p", Type: "trap", Title: title,
			Symptom:    "the sidecar container disappears after a node reboot",
			RootCause:  "the supervisor does not re-attach the sidecar",
			Resolution: "pin the sidecar and re-attach on boot",
			Body:       filler + " the sidecar container is the subject here. " + filler,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func searchPost(t *testing.T, base, tok string, body map[string]any) (int, map[string]any, []byte) {
	t.Helper()
	code, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok, body, nil)
	var out map[string]any
	if code == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, raw)
		}
	}
	return code, out, raw
}

// The default response keeps the whole entry — existing consumers read
// results[].entry.* and must not have to change.
func TestSearchDefaultViewIsBackwardCompatible(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	code, out, _ := searchPost(t, base, tok, map[string]any{"query": "sidecar", "top_k": 5})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	results, _ := out["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected hits")
	}
	first, _ := results[0].(map[string]any)
	entry, ok := first["entry"].(map[string]any)
	if !ok {
		t.Fatalf("default view dropped `entry`: %v", first)
	}
	for _, k := range []string{"id", "type", "title", "status", "project_id", "symptom"} {
		if _, ok := entry[k]; !ok {
			t.Fatalf("entry.%s missing — the dist/ scripts read it", k)
		}
	}
	// ...plus the new field, added non-destructively.
	snip, _ := first["snippet"].(string)
	if snip == "" {
		t.Fatalf("full view must also carry a snippet: %v", first)
	}
	if !strings.Contains(snip, store.SnippetOpen) {
		t.Fatalf("snippet has no highlight markers: %q", snip)
	}
}

// view:"index" projects to a flat hit and drops the entry entirely.
func TestSearchIndexViewProjection(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	code, out, _ := searchPost(t, base, tok,
		map[string]any{"query": "sidecar", "top_k": 5, "view": "index"})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	results, _ := out["results"].([]any)
	if len(results) != 5 {
		t.Fatalf("expected 5 hits, got %d", len(results))
	}
	first, _ := results[0].(map[string]any)
	if _, ok := first["entry"]; ok {
		t.Fatalf("index view must not carry `entry`: %v", first)
	}
	for _, k := range []string{
		"entry_id", "title", "type", "project_id", "updated_at", "score", "snippet",
	} {
		if _, ok := first[k]; !ok {
			t.Fatalf("index hit missing %s: %v", k, first)
		}
	}
	if id, _ := first["entry_id"].(string); id == "" {
		t.Fatal("entry_id is empty — the reader could not fetch the entry")
	}
}

// The whole point of the index view is a response an agent can read.
func TestSearchIndexViewSizeBudget(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	_, _, idxRaw := searchPost(t, base, tok,
		map[string]any{"query": "sidecar", "top_k": 5, "view": "index"})
	_, _, fullRaw := searchPost(t, base, tok,
		map[string]any{"query": "sidecar", "top_k": 5})

	if len(idxRaw) >= 3000 {
		t.Fatalf("index response is %d bytes, budget is < 3000", len(idxRaw))
	}
	if len(idxRaw) >= len(fullRaw) {
		t.Fatalf("index (%d B) is not smaller than full (%d B)", len(idxRaw), len(fullRaw))
	}
	t.Logf("top_k=5: index=%d bytes, full=%d bytes (%.1fx smaller)",
		len(idxRaw), len(fullRaw), float64(len(fullRaw))/float64(len(idxRaw)))
}

// An unknown view is a 400, never a silent fall back to "full": a typo
// would otherwise surface only as "search is inexplicably heavy".
func TestSearchUnknownViewIsRejected(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	for _, bad := range []string{"brief", "INDEX", "Full", "summary"} {
		code, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok,
			map[string]any{"query": "sidecar", "view": bad}, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("view=%q: status=%d, want 400 (body %s)", bad, code, raw)
		}
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Error.Code != CodeBadRequest {
			t.Fatalf("view=%q: code=%q, want %q", bad, env.Error.Code, CodeBadRequest)
		}
	}
	// Explicit "full" and "index" are both accepted.
	for _, ok := range []string{"full", "index"} {
		code, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
			map[string]any{"query": "sidecar", "view": ok}, nil)
		if code != http.StatusOK {
			t.Fatalf("view=%q: status=%d, want 200", ok, code)
		}
	}
}

// A zero-hit response must say so in as many words. feedback_prompt is
// hit-time wording and, next to an empty list, reads as "results are
// still coming" — a librarian in production actually waited on one.
func TestSearchZeroHitCarriesHintNotFeedbackPrompt(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	code, out, _ := searchPost(t, base, tok,
		map[string]any{"query": "qqzzxx-nothing-matches-this"})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if n, _ := out["count"].(float64); n != 0 {
		t.Fatalf("expected count 0, got %v", out["count"])
	}
	if _, ok := out["feedback_prompt"]; ok {
		t.Fatal("a zero-hit response must not carry the hit-time feedback prompt")
	}
	hint, _ := out["hint"].(string)
	if hint != ZeroHitHint {
		t.Fatalf("hint = %q, want the definitive-zero hint", hint)
	}
	if _, ok := out["match"]; !ok {
		t.Fatal("match field missing from the zero-hit response")
	}
}

// ...and a hit-bearing response keeps the prompt and adds no hint.
func TestSearchHitCarriesFeedbackPromptNotHint(t *testing.T) {
	base, tok, st := testServer(t)
	seedSearchCorpus(t, st)

	_, out, _ := searchPost(t, base, tok, map[string]any{"query": "sidecar"})
	if p, _ := out["feedback_prompt"].(string); p != FeedbackPrompt {
		t.Fatalf("feedback_prompt = %q, want the standing prompt", p)
	}
	if _, ok := out["hint"]; ok {
		t.Fatal("a hit-bearing response must not carry the zero-hit hint")
	}
	if m, _ := out["match"].(string); m != store.MatchAnd {
		t.Fatalf("match = %q, want %q", m, store.MatchAnd)
	}
}

// The AND→OR fallback is visible to the caller through `match`, so an
// agent can tell a strict hit from a widened one.
func TestSearchReportsORFallback(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ title, body string }{
		{"週次のふりかえり", "この一週間で片付いた課題をまとめる。"},
		{"作業ログ", "今日はよく頑張ったので記録しておく。"},
	} {
		if _, err := st.CreateEntry(ctx, &store.Entry{
			ProjectID: "p", Type: "note", Title: e.title, Body: e.body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, out, _ := searchPost(t, base, tok,
		map[string]any{"query": "一週間 頑張った", "view": "index"})
	if n, _ := out["count"].(float64); n == 0 {
		t.Fatalf("multi-word query still dead-ends at zero: %v", out)
	}
	if m, _ := out["match"].(string); m != store.MatchOr {
		t.Fatalf("match = %q, want %q", m, store.MatchOr)
	}
}
