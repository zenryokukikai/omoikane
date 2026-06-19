#!/usr/bin/env bash
# Find candidate near-duplicate entries for the detective to judge.
# Usage:
#   search_candidates.sh "<query phrase>" [project_id]
#
# Emits a compact JSON array of candidates on stdout:
#   [{"id":"T-XXXX","type":"trap","title":"...","score":4.3,
#     "symptom":"...","project_id":"..."}, ...]
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

PAYLOAD=$(jq -n --arg q "$QUERY" '{query: $q}')

RESP=$(curl --retry 5 --retry-connrefused -sS -X POST "$KB_URL/v1/search" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PAYLOAD")

# Flatten {results:[{entry,score}]} → compact candidate array.
# Optionally filter by project_id.
echo "$RESP" | jq --arg proj "$PROJECT" '
    [ .results[]
      | { id: .entry.id, type: .entry.type, title: .entry.title,
          score: .score, status: .entry.status,
          project_id: .entry.project_id,
          symptom: (.entry.symptom // ""),
          tags: (.entry.tags // []) }
      | select($proj == "" or .project_id == $proj)
    ]'
