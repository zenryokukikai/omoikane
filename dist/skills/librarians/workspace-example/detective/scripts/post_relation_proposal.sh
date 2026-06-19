#!/usr/bin/env bash
# Post a detective relation proposal as a librarian_meta DRAFT, then
# record progress. The detective DISCOVERS and PROPOSES; it never
# mutates the relation graph, supersedes, merges, or deletes (that is
# curator / human, gated). This script only creates a DRAFT proposal
# carrying proposed_actions[] + a progress record.
#
# Canonical vocabulary (see dist/skills/librarians/detective/): the
# proposed edges use the store's valid rel_type set:
#   related | duplicate_of | conflicts_with | see_also | depends_on
# (supersedes / resolved_by are curator/system outcomes, never
# proposed by detective.)
#
# Usage:
#   post_relation_proposal.sh <examined_entry_id> <title> <body_file_path>
#
# The body file must contain these sections (validated below):
#   ## Examined            — the entry under examination
#   ## Proposed relations  — >=1 proposal, each naming a valid rel_type
#                            and a target [[X-...]] id
#   ## Confidence          — high | medium | low (or per-proposal)
#   ## Routing             — @curator for duplicate_of / conflicts_with
#   ## Source
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

EXAMINED_ID="${1:?examined_entry_id required as first arg}"
TITLE="${2:?title required as second arg}"
BODY_FILE="${3:?body file path required as third arg}"

[[ -f "$BODY_FILE" ]] || { echo "body file not found: $BODY_FILE" >&2; exit 2; }

# --- structural validation ---
required_sections=(
    '## Examined'
    '## Proposed relations'
    '## Confidence'
    '## Routing'
    '## Source'
)
for section in "${required_sections[@]}"; do
    grep -qF "$section" "$BODY_FILE" || { echo "validation: missing section: $section" >&2; exit 3; }
done

# Must cite the examined entry.
grep -qF "[[$EXAMINED_ID]]" "$BODY_FILE" || {
    echo "validation: body must cite the examined entry [[$EXAMINED_ID]]" >&2; exit 3; }

# At least one valid rel_type must be named.
VALID_RE='related|duplicate_of|conflicts_with|see_also|depends_on'
if ! grep -qE "$VALID_RE" "$BODY_FILE"; then
    echo "validation: no valid rel_type named. Use one of: related|duplicate_of|conflicts_with|see_also|depends_on" >&2
    exit 3
fi
# Reject the stale/invalid rel_types the store rejects.
if grep -qE 'similar_to|related_to|derived_from' "$BODY_FILE"; then
    echo "validation: body uses an invalid rel_type (similar_to/related_to/derived_from). The store only accepts related|duplicate_of|conflicts_with|see_also|depends_on." >&2
    exit 3
fi

# Confidence must state high|medium|low somewhere.
grep -qiE 'high|medium|low' "$BODY_FILE" || { echo "validation: state confidence high|medium|low" >&2; exit 3; }

# Every wiki-linked id must exist; require >=1 target other than the examined entry.
referenced_ids=$(grep -oE '\[\[(T|D|X|L|I|M|F|E)-[A-Z0-9]+\]\]' "$BODY_FILE" \
    | sed 's/^\[\[//;s/\]\]$//' | sort -u)
other_count=0
for id in $referenced_ids; do
    [[ "$id" == "$EXAMINED_ID" ]] && continue
    code=$(curl --retry 5 --retry-connrefused -sS -o /dev/null -w '%{http_code}' \
        -H "Authorization: Bearer $KB_TOKEN" "$KB_URL/v1/entries/$id")
    [[ "$code" == "200" ]] || { echo "validation: cites [[$id]] which does not exist (HTTP $code). do not invent ids." >&2; exit 3; }
    other_count=$((other_count+1))
done
[[ "$other_count" -ge 1 ]] || {
    echo "validation: a proposal needs >=1 target id other than the examined entry. If nothing plausible, use post_no_action.sh." >&2; exit 3; }

BODY=$(cat "$BODY_FILE")
RELATED_JSON=$(printf '%s\n' $referenced_ids | grep -v "^$EXAMINED_ID$" | jq -R . | jq -s .)

# --- post the proposal DRAFT ---
ENTRY_PAYLOAD=$(jq -n \
    --arg title "$TITLE" --arg body "$BODY" \
    --arg role "$KB_ROLE" --arg instance "$KB_INSTANCE_ID" \
    --arg examined "$EXAMINED_ID" --argjson related "$RELATED_JSON" \
    '{
        project_id: "omoikane",
        type: "librarian_meta",
        status: "DRAFT",
        title: $title,
        body: $body,
        body_format: "markdown",
        tags: ["librarian","detective","relation_proposal"],
        metadata: {
            role: $role,
            instance_id: $instance,
            kind: "relation_proposal",
            source_entry_id: $examined,
            related_entry_ids: $related
        }
    }')

ENTRY_RESP=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$ENTRY_PAYLOAD")
DRAFT_ID=$(echo "$ENTRY_RESP" | jq -r .id)
[[ -n "$DRAFT_ID" && "$DRAFT_ID" != "null" ]] || {
    echo "failed to create proposal DRAFT — response: $ENTRY_RESP" >&2; exit 1; }

# --- progress (removes examined entry from detective backlog) ---
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/progress" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg role "$KB_ROLE" --arg examined "$EXAMINED_ID" \
        --arg instance "$KB_INSTANCE_ID" --arg draft "$DRAFT_ID" --arg title "$TITLE" \
        '{role:$role, entry_id:$examined, instance_id:$instance,
          action:"flagged_duplicate", output_entry_id:$draft,
          notes:("relation proposal: " + $title)}')" >/dev/null

# --- heartbeat ---
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$(jq -n --arg n "proposed relations for $EXAMINED_ID -> $DRAFT_ID" \
        '{note:$n, did_action:true}')" >/dev/null

jq -n --arg draft "$DRAFT_ID" --arg examined "$EXAMINED_ID" \
    '{draft_id:$draft, examined_entry_id:$examined, action:"flagged_duplicate"}'
