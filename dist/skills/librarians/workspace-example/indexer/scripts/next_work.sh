#!/usr/bin/env bash
# Emit substantive entries that still lack UseCase membership — the real
# work feed. OLDEST-uncategorised first, so the backlog drains FIFO and
# coverage is monotonic.
#
# History / why this shape: the previous version emitted NEWEST-first
# regardless of membership. The newest substantive entries were exactly the
# ones already categorised, so every session skipped all of them and
# reported "caught up" while hundreds of older entries stayed uncategorised
# forever. The fix is a server-side filter: ?uncategorized=true excludes
# anything already linked to a use_case (NOT EXISTS use_case_entries), and
# ?order=oldest drains the tail first.
#
# Usage: next_work.sh [limit]   (default 20)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

LIMIT="${1:-20}"
TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT

# Per-type so librarian_meta can't crowd the window. uncategorized=true does
# the membership filter server-side (no N+1 membership probes), order=oldest
# drains the backlog FIFO.
: > "$TMP"
for t in trap lesson decision incident design; do
    curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
        "$KB_URL/v1/entries?type=${t}&status=ACTIVE&uncategorized=true&order=oldest&limit=60" \
      | jq -c '.entries[] | {id, type, title, project_id, updated_at, status}' \
      >> "$TMP" || true
done

# Merge the per-type oldest sets, globally oldest-first, take LIMIT.
jq -rs --argjson n "$LIMIT" '
  [ .[] | select(.status == "ACTIVE") ]
  | sort_by(.updated_at)
  | .[:$n][]
  | "\(.id)\t\(.type)\t\(.project_id)\t\(.title)"
' "$TMP"
