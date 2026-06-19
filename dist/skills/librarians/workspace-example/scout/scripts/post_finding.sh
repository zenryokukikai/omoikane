#!/usr/bin/env bash
# Post a high-value scout finding: a DRAFT external_finding entry (the
# visible "worth your attention" record) PLUS a structured row in the
# external_findings table (raw log + relevance), then mark the URL seen
# so it is never re-evaluated. Heartbeat.
#
# Usage:
#   post_finding.sh <source_url> <title> <relevance 0..1> <body_file> [tags_csv]
#
# The body file (markdown) must contain these sections:
#   ## Source        — the url + where it came from (hn/arxiv)
#   ## Summary       — what it is, in a few sentences
#   ## Why it matters — novelty / value judgement (this is the point)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

URL="${1:?source_url required}"
TITLE="${2:?title required}"
RELEVANCE="${3:?relevance 0..1 required}"
BODY_FILE="${4:?body file required}"
TAGS_CSV="${5:-external,scout}"

[ -f "$BODY_FILE" ] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }
for sec in '## Source' '## Summary' '## Why it matters'; do
    grep -qF "$sec" "$BODY_FILE" || { echo "validation: missing section: $sec" >&2; exit 3; }
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BODY=$(cat "$BODY_FILE")

# Tags CSV -> JSON array
TAGS_JSON=$(jq -nR --arg s "$TAGS_CSV" '$s|split(",")|map(gsub("^\\s+|\\s+$";""))|map(select(length>0))')

# 1) DRAFT external_finding entry (visible, searchable once promoted)
ENTRY=$(jq -n --arg title "$TITLE" --arg body "$BODY" --arg url "$URL" \
    --arg instance "$KB_INSTANCE_ID" --argjson tags "$TAGS_JSON" '{
        project_id:"omoikane", type:"external_finding", status:"DRAFT",
        title:$title, body:$body, body_format:"markdown", tags:$tags,
        metadata:{role:"scout", instance_id:$instance, kind:"external_finding", source_url:$url}
    }')
RESP=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" -d "$ENTRY")
ENTRY_ID=$(echo "$RESP" | jq -r .id)
[ -n "$ENTRY_ID" ] && [ "$ENTRY_ID" != "null" ] || { echo "failed to create entry: $RESP" >&2; exit 1; }

# 2) structured finding row (raw log + relevance + dedup ledger on server)
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/findings" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg lens "$KB_ROLE" --arg inst "$KB_INSTANCE_ID" --arg url "$URL" \
        --arg title "$TITLE" --argjson rel "$RELEVANCE" --arg entry "$ENTRY_ID" \
        '{agent_lens:$lens, instance_id:$inst, source_url:$url, source_title:$title,
          relevance:$rel, metadata:({entry_id:$entry}|tostring)}')" >/dev/null || true

# 3) mark seen (SQLite) + heartbeat
python3 "$SCRIPT_DIR/seen_store.py" add posted "$URL" >/dev/null
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "posted finding $ENTRY_ID ($TITLE)" '{note:$n, did_action:true}')" >/dev/null || true

jq -n --arg id "$ENTRY_ID" --arg url "$URL" '{entry_id:$id, source_url:$url, action:"posted_finding"}'
