# omoikane tool reference

Newline-delimited JSON-RPC 2.0 over stdio. The kb-mcp adapter proxies
each `tools/call` to the local kb-server.

## Read-side tools

### `kb_lookup_by_trigger`
Find traps that apply to a planned action.
- `trigger_description` (required) — what you are about to do
- `domain` (optional) — preprocessing|training|inference|data|infra|other
- `top_k` (optional, default 10)
- `project_id` (optional)

### `kb_lookup_by_symptom`
Find entries matching an observed symptom.
- `symptom_description` (required)
- `top_k`, `project_id` (optional)

### `kb_lookup_by_situation`
Reverse-dictionary lookup from a plain-language situation.
- `situation_description` (required)
- `top_k`, `project_id` (optional)
- `create_cases` (optional, default false) — mint a `case_id` per match
  so the agent can later report outcome

### `kb_lookup_by_tags`
Tag-driven browsing.
- `tags` (required, string array)
- `match_mode` (optional) — `any` (default) or `all`
- `top_k`, `project_id` (optional)

### `kb_search`
Full-text search.
- `query` (required) — supports FTS5 syntax
- `top_k` (optional)

### `kb_get`
Fetch one entry.
- `entry_id` (required)
- `as_of` (optional) — RFC3339 timestamp for historical reconstruction

### `kb_browse`
Walk the hierarchy.
- `node_id` (optional) — empty for roots
- `include_entries` (optional) — return entries under the node
- `project_id` (optional)

### `kb_relations`
List relations for an entry.
- `entry_id` (required)
- `direction` (optional) — `outgoing` (default), `incoming`, `both`

### `kb_reflect`
Cross-entry summarisation.
- `entry_ids` (required, array)
- `prompt` (optional)

## Write-side tools

### `kb_post`
Create a new entry.
- `project_id`, `type`, `title`, `body` (required)
- `symptom`, `root_cause`, `resolution`, `prohibited`, `tags` (optional)

### `kb_feedback`
Record or update the outcome of having consulted an entry.
- `case_id` (optional) — present → PATCH; absent → POST
- `entry_id` (required when `case_id` absent)
- `trigger_query`, `outcome`, `result`, `result_evidence`, `notes`

### `kb_link`
Create a directed relation.
- `from_id`, `to_id`, `rel_type` (required)
- `confidence`, `notes` (optional)
- Valid types: `related`, `supersedes`, `conflicts_with`, `depends_on`,
  `see_also`, `duplicate_of`, `resolved_by`. Note: `conflicts_with`
  auto-supersedes the older entry, and `supersedes` marks the `to_id`
  side `SUPERSEDED`.

## Failure modes

- Transport-level kb-server failure → `{"kb_unavailable": true}` in the
  content block. The agent should continue without the KB.
- Validation/business failures → `isError: true` on the response, with
  the standard kb-server error envelope as the content.
