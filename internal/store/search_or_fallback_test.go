package store

import (
	"context"
	"testing"
)

// A natural-language query used to dead-end: every long token was AND-ed,
// so a phrase whose words live in different entries returned a bare
// count:0 even though the corpus had plenty to say. The fix retries ONCE
// with the tokens OR-ed — but only when the strict query found nothing,
// so a query that already has hits can never be diluted (issue #138).

func orFallbackStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	mk := func(title, body string) {
		t.Helper()
		if _, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "note", Title: title, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The reported case: 一週間 and 頑張った each appear, but never
	// together in one entry.
	mk("週次のふりかえり", "この一週間で片付いた課題をまとめる。")
	mk("作業ログ", "今日はよく頑張ったので記録しておく。")
	mk("無関係な記録", "全く別の話題。")
	return s, ctx
}

func TestSearchORFallbackRescuesZeroHit(t *testing.T) {
	s, ctx := orFallbackStore(t)

	// Strict AND finds nothing...
	andRes, andTotal, _, err := s.searchAND(ctx, "一週間 頑張った")
	if err != nil {
		t.Fatal(err)
	}
	if andTotal != 0 || len(andRes) != 0 {
		t.Fatalf("precondition broken: AND query already matched %d entries", andTotal)
	}

	// ...so SearchFTS retries with OR and says so.
	res, total, match, err := s.SearchFTS(ctx, "一週間 頑張った", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || len(res) == 0 {
		t.Fatal("OR fallback did not rescue the zero-hit query")
	}
	if match != MatchOr {
		t.Fatalf("match = %q, want %q", match, MatchOr)
	}
	if total != 2 {
		t.Fatalf("expected both partial matches, got total=%d", total)
	}
	for _, r := range res {
		if r.Snippet == "" {
			t.Fatal("fallback hits must carry a snippet like any other hit")
		}
	}
}

// searchAND runs only the strict pass, so a test can prove the AND query
// really was empty before the fallback fired.
func (s *Store) searchAND(ctx context.Context, q string) ([]*SearchResult, int, string, error) {
	long, short := splitFTSTokens(q)
	res, total, err := s.searchEntries(ctx, long, short, EntryFilter{}, false)
	return res, total, MatchAnd, err
}

// A query that already has hits is NOT widened — that is what keeps the
// fallback from polluting ranking on ordinary searches.
func TestSearchORFallbackDoesNotWidenAHit(t *testing.T) {
	s, ctx := orFallbackStore(t)
	// Both tokens live in the same entry, so AND matches.
	res, total, match, err := s.SearchFTS(ctx, "一週間 課題", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(res) != 1 {
		t.Fatalf("expected the single AND hit, got total=%d len=%d", total, len(res))
	}
	if match != MatchAnd {
		t.Fatalf("match = %q, want %q — a hitting query must not be widened", match, MatchAnd)
	}
}

// A single token has nothing to loosen: zero hits stay zero hits, and
// the second query is never issued.
func TestSearchSingleTokenDoesNotFallBack(t *testing.T) {
	s, ctx := orFallbackStore(t)
	res, total, match, err := s.SearchFTS(ctx, "存在しない語句", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(res) != 0 {
		t.Fatalf("expected a genuine zero, got total=%d", total)
	}
	if match != MatchAnd {
		t.Fatalf("match = %q, want %q for a single-token query", match, MatchAnd)
	}
	if orRetryApplies([]string{"存在しない語句"}, nil) {
		t.Fatal("orRetryApplies should be false for a single long token")
	}
}

// The short-token-only path (LIKE, no FTS at all) gets the same
// treatment — otherwise the fix would only work for long queries.
func TestSearchORFallbackShortTokenPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	mk := func(title, body string) {
		t.Helper()
		if _, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "note", Title: title, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("録音の記録", "音声の品質を測った。")
	mk("画像の記録", "画質の劣化を測った。")

	// Both tokens are 2 runes → LIKE path; no entry contains both.
	res, total, match, err := s.SearchFTS(ctx, "音声 画質", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(res) != 2 {
		t.Fatalf("short-token OR fallback did not fire: total=%d", total)
	}
	if match != MatchOr {
		t.Fatalf("match = %q, want %q", match, MatchOr)
	}
	for _, r := range res {
		if r.Snippet == "" {
			t.Fatal("short-token fallback hits must carry a snippet too")
		}
	}
}

// Mixed long+short: the short LIKE stays AND-ed even during the retry
// (widening it would turn the cheap filter into a full scan), so a query
// whose short token matches nothing stays at zero.
func TestSearchORFallbackKeepsShortTokensStrict(t *testing.T) {
	s, ctx := orFallbackStore(t)
	_, total, _, err := s.SearchFTS(ctx, "一週間 頑張った ZZ", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("short-token filter should still exclude everything, got total=%d", total)
	}
}

// A zero that comes from the FILTER, not the tokens, must NOT trigger the
// OR retry: widening the trigram MATCH cannot rescue a project/type/status
// that matches nothing, and OR over trigrams is expensive enough that the
// wasted pass shows up on a large corpus. The observable proof is the
// reported match mode — an "or" here would mean the retry ran for nothing.
func TestSearchORFallbackSkippedWhenFilterCausedTheZero(t *testing.T) {
	s, ctx := orFallbackStore(t)

	// The same query that DOES trigger the retry when unfiltered...
	_, total, match, err := s.SearchFTS(ctx, "一週間 頑張った", EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if match != MatchOr || total == 0 {
		t.Fatalf("precondition broken: unfiltered query should be rescued by OR, got match=%q total=%d", match, total)
	}

	// ...must stay AND when a filter no entry satisfies is what caused the zero.
	res, total, match, err := s.SearchFTS(ctx, "一週間 頑張った", EntryFilter{
		ProjectID: "no-such-project-exists",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(res) != 0 {
		t.Fatalf("filter should exclude everything, got %d", total)
	}
	if match != MatchAnd {
		t.Errorf("match = %q, want %q — the OR retry ran even though the filter caused the zero", match, MatchAnd)
	}
}
