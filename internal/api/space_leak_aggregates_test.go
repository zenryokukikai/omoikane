package api

// Leak-matrix rows, domain: aggregates — the slice-3 surface.
// Aggregates are single-space (migration 032): situations, incident
// clusters, browse (hierarchy nodes), /index, use_cases (+ synthesis),
// attachments, the findings entry-touching edge, and backlog reprocess.
//
// Fixture, runner and row conventions live in space_leak_test.go; the
// completeness guard is space_leak_guard_test.go.

var leakCasesAggregates = []leakRow{
	// ---- situations ----
	{name: "situations list", method: "GET", path: "/v1/situations", outsiderStatus: 200, memberSees: true},
	{name: "situation get", method: "GET", path: "/v1/situations/{situation}", outsiderStatus: 404, memberSees: true},
	{name: "situation create in space", method: "POST", path: "/v1/situations",
		body:           map[string]any{"description": "outsider situation", "space_id": "{spaceid}"},
		outsiderStatus: 404},
	{name: "situation add entry", method: "POST", path: "/v1/situations/{situation}/entries",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "situation remove entry", method: "DELETE",
		path: "/v1/situations/{situation}/entries/{secret}", outsiderStatus: 404},
	{name: "situation delete", method: "DELETE", path: "/v1/situations/{situation}", outsiderStatus: 404},

	// ---- incident clusters ----
	{name: "clusters list", method: "GET", path: "/v1/clusters?limit=500", outsiderStatus: 200, memberSees: true},
	{name: "cluster get", method: "GET", path: "/v1/clusters/{cluster}", outsiderStatus: 404, memberSees: true},
	{name: "cluster create in space", method: "POST", path: "/v1/clusters",
		body:           map[string]any{"title": "outsider cluster", "space_id": "{spaceid}"},
		outsiderStatus: 404},
	{name: "cluster add member", method: "POST", path: "/v1/clusters/{cluster}/members",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "cluster remove member", method: "DELETE",
		path: "/v1/clusters/{cluster}/members/{secret}", outsiderStatus: 404},
	{name: "cluster promote", method: "POST", path: "/v1/clusters/{cluster}/promote",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "cluster dismiss", method: "POST", path: "/v1/clusters/{cluster}/dismiss", outsiderStatus: 404},

	// ---- browse (hierarchy nodes) ----
	{name: "browse roots", method: "GET", path: "/v1/browse", outsiderStatus: 200, memberSees: true},
	{name: "browse node", method: "GET", path: "/v1/browse/{node}", outsiderStatus: 404, memberSees: true},
	{name: "browse node entries", method: "GET", path: "/v1/browse/{node}/entries",
		outsiderStatus: 404, memberSees: true},
	{name: "browse create in space", method: "POST", path: "/v1/browse",
		body:           map[string]any{"name": "outsider node", "space_id": "{spaceid}"},
		outsiderStatus: 404},
	{name: "browse attach entry", method: "POST", path: "/v1/browse/{node}/entries",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "browse detach entry", method: "DELETE",
		path: "/v1/browse/{node}/entries/{secret}", outsiderStatus: 404},
	{name: "browse delete node", method: "DELETE", path: "/v1/browse/{node}", outsiderStatus: 404},

	// ---- /index (cross-cutting groupings over entries + nodes) ----
	{name: "index by tag", method: "GET", path: "/v1/index?group_by=tag&limit=500",
		outsiderStatus: 200, memberSees: true, altMarker: "leakmarker-tag"},
	{name: "index by hierarchy", method: "GET", path: "/v1/index?group_by=hierarchy",
		outsiderStatus: 200, memberSees: true},
	{name: "index by recent", method: "GET", path: "/v1/index?group_by=recent", outsiderStatus: 200},

	// ---- use_cases ----
	{name: "use_cases list", method: "GET", path: "/v1/use_cases?limit=200", outsiderStatus: 200, memberSees: true},
	{name: "use_case get", method: "GET", path: "/v1/use_cases/{usecase}", outsiderStatus: 404, memberSees: true},
	{name: "use_case synthesis", method: "GET", path: "/v1/use_cases/{usecase}/synthesis",
		outsiderStatus: 404, memberSees: true},
	{name: "use_case create under hidden parent", method: "POST", path: "/v1/use_cases",
		body: map[string]any{"name_ja": "アウトサイダー", "name_en": "outsider usecase",
			"parent_id": "{usecaseid}"},
		outsiderStatus: 404},
	{name: "use_case link entry", method: "POST", path: "/v1/use_cases/{usecase}/entries",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
	{name: "use_case unlink entry", method: "DELETE",
		path: "/v1/use_cases/{usecase}/entries/{secret}", outsiderStatus: 404},
	{name: "use_case set parent", method: "POST", path: "/v1/use_cases/{usecase}/parent",
		body: map[string]any{"parent_id": ""}, outsiderStatus: 404},
	{name: "use_case delete", method: "DELETE", path: "/v1/use_cases/{usecase}", outsiderStatus: 404},

	// ---- attachments ----
	{name: "attachment get", method: "GET", path: "/v1/attachments/{attachment}",
		outsiderStatus: 404, memberSees: true},
	{name: "attachment content", method: "GET", path: "/v1/attachments/{attachment}/content",
		outsiderStatus: 404, memberSees: true},

	// ---- findings (the entry-touching edge only; the finding row
	// itself is neutral external content — see the header) ----
	{name: "findings list", method: "GET", path: "/v1/librarian/findings?limit=500", outsiderStatus: 200},
	{name: "finding correlate", method: "POST", path: "/v1/librarian/findings/{finding}/correlate",
		body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},

	// ---- backlog reprocess (silent exclusion, like /reflect) ----
	{name: "backlog reprocess", method: "POST", path: "/v1/librarian/backlog/reprocess",
		body:           map[string]any{"role": "cataloger", "entry_ids": []string{"{secretid}"}},
		outsiderStatus: 200},
}
