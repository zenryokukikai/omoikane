#!/usr/bin/env bash
# Emit substantive entries that are candidates for UseCase indexing,
# updated_at-newest first across all substantive types.
#
# Why a per-type loop: `GET /v1/entries?limit=200` returns the newest 200
# regardless of type, and librarian_meta entries dominate that window (the
# librarians post heartbeats/proposals constantly), so a plain top-200 +
# in-memory type filter yields only 1–2 substantive entries. Hit each type
# separately to bypass that.
#
# Usage: next_work.sh [limit]   (default 20)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

LIMIT="${1:-20}"
TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT

# Fetch each substantive type's most recent ACTIVE rows and accumulate.
# 60 per type more than covers a session's batch.
: > "$TMP"
for t in trap lesson decision incident design; do
    curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
        "$KB_URL/v1/entries?type=${t}&limit=60" \
      | jq -c '.entries[] | {id, type, title, project_id, updated_at, status}' \
      >> "$TMP" || true
done

# Filter to ACTIVE, sort newest-first across types, take LIMIT, emit TSV.
jq -rs --argjson n "$LIMIT" '
  [ .[] | select(.status == "ACTIVE") ]
  | sort_by(.updated_at) | reverse
  | .[:$n][]
  | "\(.id)\t\(.type)\t\(.project_id)\t\(.title)"
' "$TMP"
