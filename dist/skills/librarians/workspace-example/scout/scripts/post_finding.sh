#!/usr/bin/env bash
# Post a high-value scout finding: a DRAFT external_finding entry (the
# visible "worth your attention" record) PLUS a structured row in the
# external_findings table (raw log + relevance), then mark the URL seen
# so it is never re-evaluated. Heartbeat.
#
# Usage:
#   post_finding.sh <source_url> <title> <relevance 0..1> <body_file> [tags_csv] [ja_summary]
#
# The body file (markdown, Japanese) must contain these sections:
#   ## 出典        — url + where it came from (hn/arxiv) + original title
#   ## 課題        — the concrete problem it addresses
#   ## 何が新しいか — novelty vs existing approaches (from the source)
#   ## 手法と効果  — how it works + quantified effect (from the source)
#   ## なぜ重要か  — value judgement for the team (this is the point)
#
# ja_summary: 2–3 Japanese sentences (何か+なぜ重要か) used for the Slack
# announcement. Optional but expected — without it Slack falls back to the
# English "Why it matters" excerpt.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

URL="${1:?source_url required}"
TITLE="${2:?title required}"
RELEVANCE="${3:?relevance 0..1 required}"
BODY_FILE="${4:?body file required}"
TAGS_CSV="${5:-external,scout}"
JA_SUMMARY="${6:-}"
DIRECTIVE_ID="${7:-}"

[ -f "$BODY_FILE" ] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }
for sec in '## 出典' '## 課題' '## 何が新しいか' '## 手法と効果' '## なぜ重要か'; do
    grep -qF "$sec" "$BODY_FILE" || { echo "validation: missing section: $sec" >&2; exit 3; }
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BODY=$(cat "$BODY_FILE")

# Tags CSV -> JSON array
TAGS_JSON=$(jq -nR --arg s "$TAGS_CSV" '$s|split(",")|map(gsub("^\\s+|\\s+$";""))|map(select(length>0))')

# 1) DRAFT external_finding entry (visible, searchable once promoted)
ENTRY=$(jq -n --arg title "$TITLE" --arg body "$BODY" --arg url "$URL" \
    --arg instance "$KB_INSTANCE_ID" --argjson tags "$TAGS_JSON" \
    --arg directive "$DIRECTIVE_ID" '{
        project_id:"omoikane", type:"external_finding", status:"DRAFT",
        title:$title, body:$body, body_format:"markdown", tags:$tags,
        metadata:({role:"scout", instance_id:$instance, kind:"external_finding", source_url:$url}
                  + (if $directive != "" then {directive_id:$directive} else {} end))
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

# 3) Slack announce (best-effort; never fails the pipeline)
bash "$SCRIPT_DIR/notify_slack_finding.sh" "$ENTRY_ID" "$TITLE" "$URL" "$RELEVANCE" "$BODY_FILE" "$JA_SUMMARY" || true

# 4) mark seen (SQLite) + heartbeat
python3 "$SCRIPT_DIR/seen_store.py" add posted "$URL" >/dev/null
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "posted finding $ENTRY_ID ($TITLE)" '{note:$n, did_action:true}')" >/dev/null || true

jq -n --arg id "$ENTRY_ID" --arg url "$URL" '{entry_id:$id, source_url:$url, action:"posted_finding"}'
