package api

// Leak-matrix rows, domain: chat threads + librarian tasks — the
// slice-4 surface. intent=talk threads are owner-only, chat search
// rides /v1/search include_chat, and librarian_tasks carry the space of
// the entry an open-work claim minted them from (migration 033).
// Event-by-event SSE + webhook assertions are in
// space_leak_slice4_test.go.
//
// Fixture, runner and row conventions live in space_leak_test.go; the
// completeness guard is space_leak_guard_test.go.

var leakCasesThreads = []leakRow{
	// ---- threads (intent=talk is owner-only; coordination shared) ----
	{name: "threads list", method: "GET", path: "/v1/librarian/threads?limit=500",
		outsiderStatus: 200, memberSees: true},
	{name: "talk thread messages", method: "GET",
		path: "/v1/librarian/threads/{talkthread}/messages", outsiderStatus: 404, memberSees: true},
	{name: "talk thread close", method: "POST",
		path: "/v1/librarian/threads/{talkthread}/close", outsiderStatus: 404},
	{name: "talk thread chat post", method: "POST", path: "/v1/librarian/chat",
		body: map[string]any{"thread_id": "{talkthreadid}", "author_role": "human",
			"content": "outsider message"},
		outsiderStatus: 404},

	// ---- related_entries references (issue #103): an entry id in
	// related_entries is validated like any entry reference — an
	// invisible entry is indistinguishable from a nonexistent one
	// (uniform 404; the positive/uniformity pairs live in
	// TestThreadRelatedEntriesVisibility) ----
	{name: "thread open with invisible related entry", method: "POST",
		path: "/v1/librarian/threads",
		body: map[string]any{"title": "probe thread",
			"related_entries": `["{secretid}"]`},
		outsiderStatus: 404},
	{name: "chat post with invisible related entry", method: "POST",
		path: "/v1/librarian/chat",
		body: map[string]any{"thread_id": "{coordthreadid}", "author_role": "human",
			"content": "probe message", "related_entries": `["{secretid}"]`},
		outsiderStatus: 404},

	// ---- search with include_chat (chat_results field) ----
	{name: "search include_chat", method: "POST", path: "/v1/search",
		body:           map[string]any{"query": leakMarker, "include_chat": true},
		outsiderStatus: 200, memberSees: true},

	// ---- librarian tasks (space stamped at open-work claim, 033) ----
	{name: "librarian tasks list", method: "GET", path: "/v1/librarian/tasks?limit=500",
		outsiderStatus: 200, memberSees: true},
	{name: "task claim", method: "POST", path: "/v1/librarian/tasks/{task}/claim",
		body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
	{name: "task complete", method: "POST", path: "/v1/librarian/tasks/{task}/complete",
		body: map[string]any{"success": true}, outsiderStatus: 404},
}
