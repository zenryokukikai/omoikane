package store

import (
	"context"
	"encoding/json"
	"testing"
)

// Date-range filtering over metadata.date (issue #144).
//
// The bug this fixes: a librarian asked "what happened this week" and
// answered "there is no data" while 32 daily reports sat in the KB.
// Nothing could retrieve entries BY DATE — FTS indexes only the body
// columns, the body spells dates in Japanese, and the list had no date
// filter. TestListEntriesDateRangeFindsDailyReports pins that regression.

// seedDatedEntries creates one daily report per date plus one entry with
// no metadata.date at all, and returns the ids keyed by date.
func seedDatedEntries(t *testing.T, s *Store, dates ...string) map[string]string {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, d := range dates {
		meta, err := json.Marshal(map[string]any{
			"role": "chronicler", "kind": "org_daily", "date": d,
		})
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "librarian_meta", Status: "ACTIVE",
			// The date is spelled only in Japanese in the body, exactly
			// as the real reports do — which is why full-text search
			// cannot find them by ISO date.
			Title: "日次レポート", Body: "この日の活動まとめ",
			Metadata: json.RawMessage(meta),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[d] = id
	}
	// An entry with no metadata.date must never match a date range.
	undated, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Status: "ACTIVE",
		Title: "undated trap", Body: "no metadata at all",
	})
	if err != nil {
		t.Fatal(err)
	}
	ids["undated"] = undated
	return ids
}

func listIDs(t *testing.T, s *Store, f EntryFilter) map[string]bool {
	t.Helper()
	entries, total, err := s.ListEntries(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(entries) {
		t.Fatalf("total=%d but got %d entries (page smaller than total?)", total, len(entries))
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.ID] = true
	}
	return got
}

func TestListEntriesDateRange(t *testing.T) {
	s := newTestStore(t)
	ids := seedDatedEntries(t, s,
		"2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28",
		"2026-08-29", "2026-08-30", "2026-08-31")

	t.Run("both ends inclusive", func(t *testing.T) {
		got := listIDs(t, s, EntryFilter{DateFrom: "2026-08-25", DateTo: "2026-08-28"})
		want := []string{"2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28"}
		if len(got) != len(want) {
			t.Fatalf("got %d entries, want %d", len(got), len(want))
		}
		for _, d := range want {
			if !got[ids[d]] {
				// 25 and 28 are the boundaries: an exclusive
				// comparison would drop them.
				t.Errorf("%s missing from an inclusive range", d)
			}
		}
	})

	t.Run("entries without metadata.date never match", func(t *testing.T) {
		got := listIDs(t, s, EntryFilter{DateFrom: "2000-01-01", DateTo: "2099-12-31"})
		if got[ids["undated"]] {
			t.Error("an entry with no metadata.date matched a date range " +
				"— there must be no updated_at fallback")
		}
	})

	t.Run("date_from only", func(t *testing.T) {
		got := listIDs(t, s, EntryFilter{DateFrom: "2026-08-30"})
		if len(got) != 2 || !got[ids["2026-08-30"]] || !got[ids["2026-08-31"]] {
			t.Fatalf("date_from alone should mean everything since: got %d", len(got))
		}
	})

	t.Run("date_to only", func(t *testing.T) {
		got := listIDs(t, s, EntryFilter{DateTo: "2026-08-26"})
		if len(got) != 2 || !got[ids["2026-08-25"]] || !got[ids["2026-08-26"]] {
			t.Fatalf("date_to alone should mean everything up to: got %d", len(got))
		}
	})

	t.Run("composes with the other filters", func(t *testing.T) {
		got := listIDs(t, s, EntryFilter{
			Type: "trap", DateFrom: "2026-08-25", DateTo: "2026-08-31",
		})
		if len(got) != 0 {
			t.Fatalf("type=trap AND a date range should intersect, got %d", len(got))
		}
	})
}

// The original false negative: daily reports exist for the week, and the
// query for that week returns them. Before this change the store had no
// way to express the question at all, so the answer was always "nothing".
func TestListEntriesDateRangeFindsDailyReports(t *testing.T) {
	s := newTestStore(t)
	// Two weeks of reports. Asking for the second week must return that
	// week's five and none of the first week's — "returned something" is
	// not enough, since a filter that is silently ignored also returns
	// something (just the wrong thing).
	ids := seedDatedEntries(t, s,
		"2026-08-18", "2026-08-19", "2026-08-20", "2026-08-21", "2026-08-22",
		"2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28", "2026-08-29")

	got := listIDs(t, s, EntryFilter{
		Type:     "librarian_meta",
		DateFrom: "2026-08-25",
		DateTo:   "2026-08-31",
	})
	if len(got) == 0 {
		t.Fatal(`"what happened this week" returned nothing while daily ` +
			`reports exist for that week — the #144 false negative`)
	}
	for _, d := range []string{"2026-08-25", "2026-08-26", "2026-08-27",
		"2026-08-28", "2026-08-29"} {
		if !got[ids[d]] {
			t.Errorf("daily report for %s exists but was not returned", d)
		}
	}
	if len(got) != 5 {
		t.Fatalf("want exactly the week's 5 reports, got %d "+
			"(the previous week must not leak in)", len(got))
	}
}

// The list and the search path share dateRangeCond, so the contract has
// one definition. This pins that the search side really uses it.
func TestSearchEntriesDateRange(t *testing.T) {
	s := newTestStore(t)
	ids := seedDatedEntries(t, s, "2026-08-25", "2026-08-28", "2026-08-31")
	ctx := context.Background()

	res, _, _, err := s.SearchFTS(ctx, "日次レポート", EntryFilter{
		DateFrom: "2026-08-26", DateTo: "2026-08-30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Entry.ID != ids["2026-08-28"] {
		t.Fatalf("search+date should return only the 08-28 report, got %d hits", len(res))
	}
}
