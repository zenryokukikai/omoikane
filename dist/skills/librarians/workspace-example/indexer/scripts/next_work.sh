#!/usr/bin/env bash
# Emit substantive entries that are candidates for (re)indexing, newest
# first. The indexer reads each, decides if its reverse index is missing
# or stale, and indexes it. Signal-driven: we do NOT dump the whole KB —
# just the recent substantive entries; pair with a "seen" note in
# librarian/progress to avoid re-walking the same set every session.
#
# Usage: next_work.sh [limit]   (default 20)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

LIMIT="${1:-20}"

# Substantive knowledge types only — skip librarian_meta and thin rows.
RESP=$(curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
    "$KB_URL/v1/entries?limit=200")

echo "$RESP" | jq -r --argjson n "$LIMIT" '
  [.entries[]
   | select(.type == ("trap","lesson","decision","incident","design"))
   | select(.status == "ACTIVE")
   | {id, type, title, project_id, updated_at}]
  | .[:$n][]
  | "\(.id)\t\(.type)\t\(.project_id)\t\(.title)"'
