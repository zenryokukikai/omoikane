#!/usr/bin/env bash
# Record that cataloger inspected this entry and chose not to act.
# Usage:
#   post_no_action.sh <source_entry_id> <reason>
# This removes the entry from the cataloger backlog without writing a
# summary. Use sparingly — `no_action` should mean "I genuinely have
# nothing to add", not "this is hard".
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

SOURCE_ID="${1:?source_entry_id required}"
REASON="${2:-no further organization needed}"

PROGRESS_PAYLOAD=$(jq -n \
    --arg role "$KB_ROLE" \
    --arg source "$SOURCE_ID" \
    --arg instance "$KB_INSTANCE_ID" \
    --arg reason "$REASON" \
    '{role: $role, entry_id: $source, instance_id: $instance,
      action: "no_action", notes: $reason}')

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/progress" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PROGRESS_PAYLOAD" >/dev/null

# heartbeat
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "no_action on $SOURCE_ID: $REASON" \
        '{note:$n, did_action:false}')" >/dev/null

jq -n --arg source "$SOURCE_ID" --arg reason "$REASON" \
    '{source_entry_id: $source, action: "no_action", notes: $reason}'
