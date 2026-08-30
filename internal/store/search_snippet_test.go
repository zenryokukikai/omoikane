package store

import (
	"context"
	"strings"
	"testing"
)

// Every search hit must carry an excerpt around the match (issue #138):
// a bare list of ids and titles does not let a reader decide what to
// open. Two code paths produce it — SQLite's snippet() on the FTS path
// and a Go-built excerpt on the short-token-only path — and these tests
// hold both to the same contract, because callers do not branch on
// which one ran.

func snippetTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

// FTS path: the matched term comes back wrapped in the markers.
func TestSnippetFTSPathHighlightsMatch(t *testing.T) {
	s, ctx := snippetTestStore(t)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "Mask trap",
		Body: "A long preamble that exists only so the excerpt has to cut " +
			"something off the front before it reaches the interesting part. " +
			"The rectangular artifact appears whenever preprocessing differs.",
	}); err != nil {
		t.Fatal(err)
	}
	res, _, _, err := s.SearchFTS(ctx, "rectangular", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
	snip := res[0].Snippet
	if snip == "" {
		t.Fatal("FTS hit came back with an empty snippet")
	}
	if !strings.Contains(snip, SnippetOpen+"rectangular"+SnippetClose) {
		t.Fatalf("match not wrapped in markers: %q", snip)
	}
	// The excerpt is a window, not the whole body: the far end of the
	// text must not be dragged along.
	if strings.Contains(snip, "long preamble that exists only") {
		t.Fatalf("snippet returned the whole body instead of a window: %q", snip)
	}
}

// FTS column -1 picks the column that actually matched, so a body hit
// yields body context rather than the head of the title.
func TestSnippetFTSPicksMatchingColumn(t *testing.T) {
	s, ctx := snippetTestStore(t)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "Completely unrelated heading",
		Body:  "the deployment pipeline drops the sidecar container on restart",
	}); err != nil {
		t.Fatal(err)
	}
	res, _, _, err := s.SearchFTS(ctx, "sidecar", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
	snip := res[0].Snippet
	if !strings.Contains(snip, SnippetOpen+"sidecar"+SnippetClose) {
		t.Fatalf("expected the body match highlighted, got %q", snip)
	}
	if !strings.Contains(snip, "container") {
		t.Fatalf("expected body context around the match, got %q", snip)
	}
}

// Snippets stay bounded: a 4KB body must not come back in full, or the
// whole point of the index view (a response an agent can read) is lost.
func TestSnippetIsBounded(t *testing.T) {
	s, ctx := snippetTestStore(t)
	filler := strings.Repeat("filler words for padding the body out. ", 120)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "Padded", Body: filler + "needle " + filler,
	}); err != nil {
		t.Fatal(err)
	}
	res, _, _, err := s.SearchFTS(ctx, "needle", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
	n := len([]rune(res[0].Snippet))
	if n > 400 {
		t.Fatalf("snippet is %d runes, expected a bounded window: %q", n, res[0].Snippet)
	}
	// ...and wide enough to be worth reading. entries_fts is TRIGRAM
	// tokenized, so snippet()'s token budget counts three-character
	// trigrams, not words: a word-sized budget would silently collapse
	// the window to ~18 characters. This lower bound is what catches
	// that regression.
	if n < 40 {
		t.Fatalf("snippet is only %d runes (%q) — too narrow to judge "+
			"relevance; check the snippet() token budget", n, res[0].Snippet)
	}
}

// Short-token-only queries never touch entries_fts, so snippet() is not
// available — the excerpt is built in Go. Same markers, same contract.
func TestSnippetShortTokenPathBuildsExcerpt(t *testing.T) {
	s, ctx := snippetTestStore(t)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "音声合成の記録",
		Body: "評価メモをここに長く書いておく。前後の文脈が十分に取れるだけの分量を用意する。" +
			"音声モデルの品質は入力の正規化に強く依存する。",
	}); err != nil {
		t.Fatal(err)
	}
	// 2 runes → short token → LIKE path (useFTS == false).
	res, _, _, err := s.SearchFTS(ctx, "音声", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
	snip := res[0].Snippet
	if !strings.Contains(snip, SnippetOpen+"音声"+SnippetClose) {
		t.Fatalf("short-token path produced no highlighted excerpt: %q", snip)
	}
	if n := len([]rune(snip)); n > excerptWindow+40 {
		t.Fatalf("excerpt window is %d runes, want <= %d", n, excerptWindow+40)
	}
}

// Case-insensitive matching on the Go path, and the excerpt must quote
// the ORIGINAL casing back (it is shown to a reader, not re-matched).
func TestSnippetShortTokenCaseInsensitive(t *testing.T) {
	s, ctx := snippetTestStore(t)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "Report", Body: "the GC pause dominates the tail latency",
	}); err != nil {
		t.Fatal(err)
	}
	res, _, _, err := s.SearchFTS(ctx, "gc", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
	if !strings.Contains(res[0].Snippet, SnippetOpen+"GC"+SnippetClose) {
		t.Fatalf("expected original casing highlighted, got %q", res[0].Snippet)
	}
}

// Whatever the path, Snippet is never empty — callers are promised they
// can render it without a nil check or a fallback of their own.
func TestSnippetAlwaysPopulated(t *testing.T) {
	s, ctx := snippetTestStore(t)
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap",
		Title: "接続の設計", Body: "ステートレス方向へ正式化された。GC pause も測る。",
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ステートレス", "GC", "pause 測る"} {
		res, _, _, err := s.SearchFTS(ctx, q, EntryFilter{})
		if err != nil {
			t.Fatalf("SearchFTS(%q): %v", q, err)
		}
		for _, r := range res {
			if r.Snippet == "" {
				t.Fatalf("SearchFTS(%q) returned a hit with no snippet", q)
			}
		}
	}
}

// entryExcerpt's last resort: no query token occurs in the entry at all
// (possible only if the caller passes tokens that never matched). It
// must still return readable text rather than an empty string.
func TestEntryExcerptFallsBackToHead(t *testing.T) {
	e := &Entry{Title: "Head", Body: strings.Repeat("body text ", 40)}
	got := entryExcerpt(e, []string{"absent"})
	if got == "" {
		t.Fatal("fallback excerpt is empty")
	}
	if !strings.HasPrefix(got, "Head") {
		t.Fatalf("fallback should start at the title, got %q", got)
	}
	if n := len([]rune(got)); n > excerptWindow+1 {
		t.Fatalf("fallback excerpt is %d runes, want <= %d", n, excerptWindow+1)
	}
}
