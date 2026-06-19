#!/usr/bin/env bash
# Post a curator resolution as a librarian_meta DRAFT, then record
# progress against the entry the curator examined.
#
# The curator RESOLVES; it does not mutate. Like every Phase-5
# librarian, its output is a DRAFT proposal (proposed_actions: supersede
# / synthesize / coexist / reject / archive_draft / ...). A human or a
# Phase-6 actor executes. This script creates the DRAFT and records one
# progress row so the examined entry leaves curator's backlog.
#
# Usage:
#   post_resolution.sh <examined_entry_id> <action> <title> <body_file>
# where:
#   examined_entry_id = the entry curator processed this tick. For a
#       detective proposal this is the proposal's librarian_meta id
#       (kind=relation_proposal); for a status case it's the entry.
#   action            = progress action verb, one of:
#       resolved_supersede | resolved_synthesize | resolved_coexist |
#       rejected_proposal  | status_proposal     | no_action(use the
#       other script for plain no_action)
#   title             = short agent-readable title
#   body_file         = markdown body (sections validated below)
#
# Required body sections:
#   ## Examined          — what was reviewed (entry/proposal id, cited)
#   ## Verdict           — confirm / reject (+ the relationship type)
#   ## Proposed actions  — supersede/synthesize/coexist/reject/status…
#   ## Rationale         — why, citing the entries' own content
#   ## Source
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

EXAMINED_ID="${1:?examined_entry_id required}"
ACTION="${2:?action verb required}"
TITLE="${3:?title required}"
BODY_FILE="${4:?body file path required}"

[[ -f "$BODY_FILE" ]] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }

case "$ACTION" in
    resolved_supersede|resolved_synthesize|resolved_coexist|approved_relation|rejected_proposal|status_proposal) ;;
    *) echo "invalid action: $ACTION (resolved_supersede|resolved_synthesize|resolved_coexist|approved_relation|rejected_proposal|status_proposal)" >&2; exit 2 ;;
esac

required_sections=(
    '## Examined'
    '## Verdict'
    '## Proposed actions'
    '## Rationale'
    '## Source'
)
for section in "${required_sections[@]}"; do
    grep -qF "$section" "$BODY_FILE" || { echo "validation: missing section: $section" >&2; exit 3; }
done

grep -qF "[[$EXAMINED_ID]]" "$BODY_FILE" || {
    echo "validation: body must cite the examined entry/proposal [[$EXAMINED_ID]]" >&2; exit 3; }

# Every wiki-linked id must exist (catch hallucinated ids).
referenced_ids=$(grep -oE '\[\[(T|D|X|L|I|M|F|E)-[A-Z0-9]+\]\]' "$BODY_FILE" \
    | sed 's/^\[\[//;s/\]\]$//' | sort -u)
for id in $referenced_ids; do
    code=$(curl --retry 5 --retry-connrefused -sS -o /dev/null -w '%{http_code}' \
        -H "Authorization: Bearer $KB_TOKEN" "$KB_URL/v1/entries/$id")
    [[ "$code" == "200" ]] || { echo "validation: cites [[$id]] which does not exist (HTTP $code). do not invent ids." >&2; exit 3; }
done

BODY=$(cat "$BODY_FILE")
RELATED_JSON=$(printf '%s\n' $referenced_ids | grep -v "^$EXAMINED_ID$" | jq -R . | jq -s .)

ENTRY_PAYLOAD=$(jq -n \
    --arg title "$TITLE" --arg body "$BODY" \
    --arg role "$KB_ROLE" --arg instance "$KB_INSTANCE_ID" \
    --arg examined "$EXAMINED_ID" --arg action "$ACTION" \
    --argjson related "$RELATED_JSON" \
    '{
        project_id: "omoikane",
        type: "librarian_meta",
        status: "DRAFT",
        title: $title,
        body: $body,
        body_format: "markdown",
        tags: ["librarian","curator","resolution"],
        metadata: {
            role: $role,
            instance_id: $instance,
            kind: "curator_resolution",
            resolution_action: $action,
            source_entry_id: $examined,
            related_entry_ids: $related
        }
    }')

ENTRY_RESP=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$ENTRY_PAYLOAD")
DRAFT_ID=$(echo "$ENTRY_RESP" | jq -r .id)
[[ -n "$DRAFT_ID" && "$DRAFT_ID" != "null" ]] || {
    echo "failed to create resolution DRAFT — response: $ENTRY_RESP" >&2; exit 1; }

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/progress" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg role "$KB_ROLE" --arg examined "$EXAMINED_ID" \
        --arg instance "$KB_INSTANCE_ID" --arg draft "$DRAFT_ID" \
        --arg action "$ACTION" --arg title "$TITLE" \
        '{role:$role, entry_id:$examined, instance_id:$instance,
          action:$action, output_entry_id:$draft,
          notes:("resolution: " + $title)}')" >/dev/null

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "$ACTION on $EXAMINED_ID -> $DRAFT_ID" '{note:$n, did_action:true}')" >/dev/null

jq -n --arg draft "$DRAFT_ID" --arg examined "$EXAMINED_ID" --arg action "$ACTION" \
    '{draft_id:$draft, examined_entry_id:$examined, action:$action}'
