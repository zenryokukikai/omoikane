package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// date_from / date_to on GET /v1/entries (issue #144).
//
// A bad date is a 400, never a silent ignore: quietly dropping the
// filter is what let a librarian conclude "there is no data" while the
// daily reports were sitting right there.

func seedDatedAPIEntries(t *testing.T, st *store.Store, dates ...string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range dates {
		meta, err := json.Marshal(map[string]any{"kind": "org_daily", "date": d})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateEntry(ctx, &store.Entry{
			ProjectID: "p", Type: "librarian_meta", Status: "ACTIVE",
			Title: "日次レポート " + d, Body: "この日の活動まとめ",
			Metadata: json.RawMessage(meta),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func listEntriesQuery(t *testing.T, base, tok, query string) (int, map[string]any) {
	t.Helper()
	code, raw := doJSON(t, http.MethodGet, base+"/v1/entries?"+query, tok, nil, nil)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return code, out
}

func TestListEntriesDateParamValidation(t *testing.T) {
	base, tok, st := testServer(t)
	seedDatedAPIEntries(t, st, "2026-08-25", "2026-08-26", "2026-08-27", "2026-08-31")

	bad := []struct{ name, query string }{
		{"impossible date", "date_from=2026-13-99"},
		{"not a date at all", "date_from=last%20week"},
		{"datetime is not accepted", "date_to=2026-08-25T00:00:00Z"},
		{"slashes", "date_to=2026%2F08%2F25"},
		{"reversed range", "date_from=2026-08-31&date_to=2026-08-01"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			code, out := listEntriesQuery(t, base, tok, c.query)
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400 (a silently ignored filter "+
					"is the bug #144 exists to fix)", code)
			}
			errObj, _ := out["error"].(map[string]any)
			if errObj["code"] != CodeBadRequest {
				t.Fatalf("code=%v, want %s", errObj["code"], CodeBadRequest)
			}
		})
	}

	t.Run("valid range returns the matching entries", func(t *testing.T) {
		code, out := listEntriesQuery(t, base, tok,
			"date_from=2026-08-25&date_to=2026-08-27")
		if code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
		entries, _ := out["entries"].([]any)
		if len(entries) != 3 {
			t.Fatalf("got %d entries, want the 3 in range", len(entries))
		}
		pag, _ := out["pagination"].(map[string]any)
		if pag["total"].(float64) != 3 {
			t.Fatalf("total=%v, want 3 — the count must reflect the filter",
				pag["total"])
		}
	})

	t.Run("equal ends select that one day", func(t *testing.T) {
		code, out := listEntriesQuery(t, base, tok,
			"date_from=2026-08-26&date_to=2026-08-26")
		if code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
		entries, _ := out["entries"].([]any)
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want exactly the one dated 08-26", len(entries))
		}
	})

	t.Run("no date params leaves the list unfiltered", func(t *testing.T) {
		code, out := listEntriesQuery(t, base, tok, "")
		if code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
		entries, _ := out["entries"].([]any)
		if len(entries) != 4 {
			t.Fatalf("got %d, want all 4 — an absent filter must not narrow",
				len(entries))
		}
	})
}

// The search surface shares the same validation helper, so a bad date
// there is a 400 too, and a good one narrows the hits.
func TestSearchDateFilter(t *testing.T) {
	base, tok, st := testServer(t)
	seedDatedAPIEntries(t, st, "2026-08-25", "2026-08-28", "2026-08-31")

	code, _, _ := searchPost(t, base, tok, map[string]any{
		"query":   "日次レポート",
		"filters": map[string]any{"date_from": "nonsense"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad filters.date_from: status=%d, want 400", code)
	}

	code, out, _ := searchPost(t, base, tok, map[string]any{
		"query": "日次レポート",
		"filters": map[string]any{
			"date_from": "2026-08-26", "date_to": "2026-08-30",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	results, _ := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("got %d hits, want only the 08-28 report", len(results))
	}
}

// A hit found by date is unreadable without its date, so the index view
// carries metadata.date (issue #144 on top of the #138 budget).
func TestSearchIndexViewCarriesDate(t *testing.T) {
	base, tok, st := testServer(t)
	seedDatedAPIEntries(t, st, "2026-08-28")

	code, out, _ := searchPost(t, base, tok, map[string]any{
		"query": "日次レポート", "view": ViewIndex,
	})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	results, _ := out["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected a hit")
	}
	hit, _ := results[0].(map[string]any)
	if hit["date"] != "2026-08-28" {
		t.Fatalf("index hit date=%v, want 2026-08-28", hit["date"])
	}

	// Entries with no metadata.date must not sprout an empty field —
	// `omitempty` keeps the per-hit budget honest.
	ctx := context.Background()
	if _, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Status: "ACTIVE",
		Title: "undated sidecar trap", Body: "no metadata here at all",
	}); err != nil {
		t.Fatal(err)
	}
	code, out, _ = searchPost(t, base, tok, map[string]any{
		"query": "sidecar", "view": ViewIndex,
	})
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	results, _ = out["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected a hit")
	}
	hit, _ = results[0].(map[string]any)
	if _, present := hit["date"]; present {
		t.Fatalf("undated hit carries a date field: %v", hit)
	}
}
