#!/usr/bin/env bash
# Link an entry to a UseCase. Idempotent — re-linking the same pair is a no-op.
# Usage: link_use_case.sh <use_case_id_or_slug> <entry_id>
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

REF="${1:?use_case id or slug required}"
ENTRY_ID="${2:?entry_id required}"

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/use_cases/$REF/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg eid "$ENTRY_ID" --arg src "indexer:${KB_INSTANCE_ID}" \
          '{entry_id:$eid, source:$src}')"

# Heartbeat with what we just linked.
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "linked use_case=$REF entry=$ENTRY_ID" \
          '{note:$n, did_action:true}')" >/dev/null || true
