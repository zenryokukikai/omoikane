#!/usr/bin/env bash
# Post the daily journal as ONE ACTIVE entry. Idempotent per day: if a
# daily_journal for <date> already exists, refuse (exit 4) so a re-run
# doesn't create a duplicate.
#
# Usage: post_journal.sh <date YYYY-MM-DD> <body_file>
#
# The journal is type=librarian_meta, kind=daily_journal, status=ACTIVE
# (the one sanctioned ACTIVE write by a Phase-5 librarian — it exists to
# be read and searched immediately; see the bundle's status exception).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

DATE="${1:?date YYYY-MM-DD required}"
BODY_FILE="${2:?body file required}"
[ -f "$BODY_FILE" ] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }

TITLE="omoikane daily journal — ${DATE}"

# Idempotency: a daily journal is ACTIVE, so search finds it.
EXISTING=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/search" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg q "daily journal $DATE" '{query:$q}')" \
    | jq -r --arg t "$TITLE" '[.results[].entry | select(.title==$t)] | length' 2>/dev/null || echo 0)
if [ "${EXISTING:-0}" != "0" ]; then
    echo "a daily journal for $DATE already exists — refusing to duplicate" >&2
    exit 4
fi

BODY=$(cat "$BODY_FILE")
ENTRY=$(jq -n --arg title "$TITLE" --arg body "$BODY" --arg date "$DATE" \
    --arg instance "$KB_INSTANCE_ID" '{
        project_id:"omoikane", type:"librarian_meta", status:"ACTIVE",
        title:$title, body:$body, body_format:"markdown",
        tags:["journal","daily","summarizer"],
        metadata:{role:"summarizer", instance_id:$instance, kind:"daily_journal", journal_date:$date}
    }')
RESP=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" -d "$ENTRY")
ID=$(echo "$RESP" | jq -r .id)
[ -n "$ID" ] && [ "$ID" != "null" ] || { echo "failed to create journal: $RESP" >&2; exit 1; }

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "posted daily journal $ID for $DATE" '{note:$n, did_action:true}')" >/dev/null || true

jq -n --arg id "$ID" --arg date "$DATE" '{journal_id:$id, date:$date, status:"ACTIVE"}'
