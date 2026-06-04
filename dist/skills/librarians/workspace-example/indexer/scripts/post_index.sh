#!/usr/bin/env bash
# Write the reverse-lookup index for one entry. Takes a JSON body file
# with {symptoms[], triggers[{phrase,domain}]} — source is stamped here.
# Idempotent per dimension (the server REPLACEs symptoms/triggers).
#
# Usage: post_index.sh <entry_id> <body_file.json>
#
# body_file example:
#   {
#     "symptoms": ["音声にノイズ", "audio noise after resume"],
#     "triggers": [{"phrase":"training resume noise","domain":"audio"},
#                  {"phrase":"再開後のノイズ","domain":"audio"}]
#   }
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

ENTRY_ID="${1:?entry_id required}"
BODY_FILE="${2:?body file required}"
[ -f "$BODY_FILE" ] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }

# Inject source = this instance, so index rows are auditable.
PAYLOAD=$(jq --arg src "indexer:${KB_INSTANCE_ID}" '. + {source: $src}' "$BODY_FILE")

RESP=$(curl -fsS -X POST "$KB_URL/v1/entries/${ENTRY_ID}/index" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PAYLOAD")
echo "$RESP"

# Heartbeat with what we indexed.
S=$(echo "$RESP" | jq -r '.symptoms // 0')
T=$(echo "$RESP" | jq -r '.triggers // 0')
curl -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "indexed $ENTRY_ID (symptoms:$S triggers:$T)" '{note:$n, did_action:true}')" \
    >/dev/null || true
