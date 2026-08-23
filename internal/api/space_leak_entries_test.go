package api

// Leak-matrix rows, domain: entries and their descendants — the slice-2
// surface. Entry CRUD + history/relations/summary, comments, cases,
// feedback/engagement/signals, search, reverse-index lookups, reflect,
// tiers, review queue, coordinator triage, backlog next, per-user
// projections, librarian progress, and the open-work loop.
//
// Fixture, runner and row conventions live in space_leak_test.go; the
// completeness guard is space_leak_guard_test.go.

import "time"

// leakAsOf is a future as_of for the temporal-read row.
var leakAsOf = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

var leakCasesEntries = []leakRow{
	// ---- entries + descendants ----
	{name: "entries list", method: "GET", path: "/v1/entries?limit=500", outsiderStatus: 200, memberSees: true},
	{name: "entries list q-filter", method: "GET", path: "/v1/entries?q=" + leakMarker, outsiderStatus: 200, memberSees: true},
	{name: "entry get", method: "GET", path: "/v1/entries/{secret}", outsiderStatus: 404, memberSees: true},
	{name: "entry get as_of", method: "GET", path: "/v1/entries/{secret}?as_of=" + leakAsOf, outsiderStatus: 404, memberSees: true},
	{name: "entry history", method: "GET", path: "/v1/entries/{secret}/history", outsiderStatus: 404, memberSees: true},
	{name: "entry relations", method: "GET", path: "/v1/entries/{secret}/relations?direction=both", outsiderStatus: 404},
	{name: "relations from internal entry", method: "GET", path: "/v1/entries/{internal}/relations?direction=both", outsiderStatus: 200, memberSees: true, idOnly: true},
	{name: "entry summary", method: "GET", path: "/v1/entries/{secret}/summary", outsiderStatus: 404},
	{name: "entry comments", method: "GET", path: "/v1/entries/{secret}/comments", outsiderStatus: 404, memberSees: true},
	{name: "entry cases", method: "GET", path: "/v1/entries/{secret}/cases", outsiderStatus: 404, memberSees: true},
	{name: "entry use_cases", method: "GET", path: "/v1/entries/{secret}/use_cases", outsiderStatus: 404},
	{name: "entry engagement", method: "GET", path: "/v1/entries/{secret}/engagement", outsiderStatus: 404, memberSees: true, idOnly: true},
	{name: "entry signals", method: "GET", path: "/v1/entries/{secret}/signals", outsiderStatus: 404, memberSees: true},

	// ---- search (candidate-stage filter; count too) ----
	{name: "search", method: "POST", path: "/v1/search",
		body: map[string]any{"query": leakMarker}, outsiderStatus: 200, memberSees: true},

	// ---- lookups ----
	{name: "lookup by-symptom", method: "POST", path: "/v1/lookup/by-symptom",
		body:           map[string]any{"symptom_description": leakMarker + " indexed symptom phrase"},
		outsiderStatus: 200, memberSees: true},
	{name: "lookup by-trigger", method: "POST", path: "/v1/lookup/by-trigger",
		body:           map[string]any{"trigger_description": leakMarker + " indexed trigger phrase"},
		outsiderStatus: 200, memberSees: true},
	{name: "lookup by-tags", method: "POST", path: "/v1/lookup/by-tags",
		body:           map[string]any{"tags": []string{"leakmarker-tag"}},
		outsiderStatus: 200, memberSees: true},
	{name: "lookup by-situation", method: "POST", path: "/v1/lookup/by-situation",
		body:           map[string]any{"situation_description": "wholly neutral situation description"},
		outsiderStatus: 200, memberSees: true},

	// ---- reflect (silent exclusion oracle) ----
	{name: "reflect", method: "POST", path: "/v1/reflect",
		body:           map[string]any{"entry_ids": []string{"{secretid}"}},
		outsiderStatus: 200, memberSees: true},

	// ---- tiers (entry bodies grouped by usage tier; the 3 planted
	// misleading cases put the secret entry in tier 4) ----
	{name: "tiers", method: "GET", path: "/v1/tiers?tier=4&limit=500", outsiderStatus: 200, memberSees: true},

	// ---- review queue + coordinator triage (misleading-heavy) ----
	{name: "review queue", method: "GET", path: "/v1/review-queue", outsiderStatus: 200, memberSees: true},
	{name: "coordinator triage", method: "GET", path: "/v1/librarian/coordinator/triage",
		outsiderStatus: 200, memberSees: true, idOnly: true},

	// ---- cross-entry comment feed ----
	{name: "recent comments", method: "GET", path: "/v1/comments/recent", outsiderStatus: 200, memberSees: true},

	// ---- librarian backlog (returns a FULL entry; detective has no
	// progress rows, so the member's oldest unprocessed = secret) ----
	{name: "backlog next", method: "GET", path: "/v1/librarian/backlog/next?role=detective",
		outsiderStatus: 200, memberSees: true},

	// ---- per-user projections ----
	{name: "my bookmarks", method: "GET", path: "/v1/me/bookmarks", outsiderStatus: 200, memberSees: true},
	{name: "my review-requests", method: "GET", path: "/v1/me/review-requests", outsiderStatus: 200, memberSees: true},

	// ---- cases + librarian progress ----
	{name: "case get", method: "GET", path: "/v1/cases/{case}", outsiderStatus: 404, memberSees: true},
	{name: "librarian progress", method: "GET", path: "/v1/librarian/progress?role=cataloger", outsiderStatus: 200, memberSees: true},

	// ---- open work ----
	{name: "open_work list", method: "GET", path: "/v1/open_work", outsiderStatus: 200, memberSees: true},

	// ---- writes: 404 for the outsider, never a leaked byte ----
	{name: "entry create in space", method: "POST", path: "/v1/entries",
		body: map[string]any{"project_id": "p-leak", "type": "lesson",
			"title": "outsider write", "body": "outsider write", "space_id": "{spaceid}"},
		outsiderStatus: 404},
	{name: "entry patch", method: "PATCH", path: "/v1/entries/{secret}",
		body: map[string]any{"title": "patched"}, header: map[string]string{"If-Match": "1"},
		outsiderStatus: 404},
	{name: "entry delete", method: "DELETE", path: "/v1/entries/{secret}", outsiderStatus: 404},
	{name: "entry index write", method: "POST", path: "/v1/entries/{secret}/index",
		body: map[string]any{"symptoms": []string{"outsider phrase"}}, outsiderStatus: 404},
	{name: "entry comment post", method: "POST", path: "/v1/entries/{secret}/comments",
		body: map[string]any{"body": "outsider comment"}, outsiderStatus: 404},
	{name: "comment patch", method: "PATCH", path: "/v1/comments/{comment}",
		body: map[string]any{"resolved": true}, outsiderStatus: 404},
	{name: "comment delete", method: "DELETE", path: "/v1/comments/{comment}", outsiderStatus: 404},
	{name: "bookmark put", method: "PUT", path: "/v1/entries/{secret}/bookmark", outsiderStatus: 404},
	{name: "bookmark delete", method: "DELETE", path: "/v1/entries/{secret}/bookmark", outsiderStatus: 404},
	{name: "feedback post", method: "POST", path: "/v1/feedback",
		body: map[string]any{"entry_id": "{secretid}", "signal": "helpful"}, outsiderStatus: 404},
	{name: "case create", method: "POST", path: "/v1/cases",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "case patch", method: "PATCH", path: "/v1/cases/{case}",
		body: map[string]any{"notes": "outsider note"}, outsiderStatus: 404},
	{name: "relation create", method: "POST", path: "/v1/relations",
		body:           map[string]any{"from_id": "{internalid}", "to_id": "{secretid}", "rel_type": "related"},
		outsiderStatus: 404},
	{name: "relation delete", method: "DELETE",
		path:           "/v1/relations?from_id={internal}&to_id={secret}&rel_type=see_also",
		outsiderStatus: 404},
	// Claim: a hidden entry takes exactly the missing-entry path
	// (409 "not tagged open") so probing cannot distinguish
	// restricted ids from nonexistent ones.
	{name: "open_work claim", method: "POST", path: "/v1/entries/{secret}/claim",
		body: map[string]any{"role": "cataloger", "instance_id": "i-leaktest"}, outsiderStatus: 409},
	{name: "open_work release", method: "POST", path: "/v1/entries/{secret}/release",
		body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
	{name: "open_work mark_merged", method: "POST", path: "/v1/entries/{secret}/mark_merged",
		body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
	{name: "librarian progress post", method: "POST", path: "/v1/librarian/progress",
		body:           map[string]any{"role": "cataloger", "entry_id": "{secretid}", "action": "summarize"},
		outsiderStatus: 404},
}
