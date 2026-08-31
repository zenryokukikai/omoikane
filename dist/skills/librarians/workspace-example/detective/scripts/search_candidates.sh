#!/usr/bin/env bash
# Find candidate near-duplicate entries for the detective to judge.
# Usage:
#   search_candidates.sh "<query phrase>" [project_id]
#
# Emits a compact JSON array of candidates on stdout:
#   [{"id":"T-XXXX","type":"trap","title":"...","score":4.3,
#     "project_id":"...","snippet":"…«match» と前後の文脈…"}, ...]
#
# `snippet` is the text the query actually matched, with the hit
# wrapped in « »: it is what lets you judge relevance — and quote a
# concrete shared claim — without opening the entry. Open the ones
# that look right with GET /v1/entries/{id}.
#
# This is a COARSE candidate generator (FTS5 lexical search). It is
# deliberately generous — the detective LLM does the real semantic
# judgement on whatever this returns. Cross-language duplicates will
# NOT surface here (lexical search can't bridge ja<->en); for those
# the detective should issue multiple queries with translated key
# terms. That translation is the LLM's job, not this script's.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

QUERY="${1:?query phrase required as first arg}"
PROJECT="${2:-}"

# view:"index" (issue #138) asks for the flat, lightweight hit —
# entry_id / title / type / project_id / updated_at / score / snippet —
# instead of the whole entry. Same 50-hit result set: ~15 KB instead of
# ~150 KB. `status`, `symptom` and `tags` are not in that projection and
# are no longer printed: `status` was read by nobody (search already
# excludes SUPERSEDED/ARCHIVED/DUPLICATE), `symptom` is empty on ~90% of
# hits (only traps/incidents carry one), and `tags` are explicitly NOT
# evidence per SKILL.md. `snippet` replaces all three with the one thing
# the judging step actually needs: the matched text.
PAYLOAD=$(jq -n --arg q "$QUERY" '{query: $q, view: "index"}')

RESP=$(curl --retry 5 --retry-connrefused -sS -X POST "$KB_URL/v1/search" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PAYLOAD")

# Search-level signals go to STDERR so stdout stays a plain JSON array:
#   count:0    → print the server's hint. That zero is definitive
#                ("一致ゼロ"), not "results still loading".
#   match:"or" → the strict AND matched nothing and the query was
#                retried with OR, so the hits are looser than usual.
COUNT=$(printf '%s' "$RESP" | jq -r '.count // 0')
if [[ "$COUNT" == "0" ]]; then
    printf 'search: %s\n' \
        "$(printf '%s' "$RESP" | jq -r '.hint // "no hits"')" >&2
elif [[ "$(printf '%s' "$RESP" | jq -r '.match // "and"')" == "or" ]]; then
    printf 'search: match=or — AND matched nothing, so the query was retried with OR; these hits are looser than usual.\n' >&2
fi

# Flatten {results:[{entry_id,title,…,snippet}]} → compact candidate array.
# Optionally filter by project_id.
echo "$RESP" | jq --arg proj "$PROJECT" '
    [ .results[]
      | { id: .entry_id, type: .type, title: .title,
          score: .score,
          project_id: .project_id,
          snippet: (.snippet // "") }
      | select($proj == "" or .project_id == $proj)
    ]'
