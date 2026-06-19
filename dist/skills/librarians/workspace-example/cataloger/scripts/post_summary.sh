#!/usr/bin/env bash
# Post a cataloger summary librarian_meta DRAFT, then record progress.
# Usage:
#   post_summary.sh <source_entry_id> <title> <body_file_path>
# Where:
#   source_entry_id = the entry the summary covers (e.g. T-XXXXXX)
#   title           = short subject (5-10 words, agent-readable)
#   body_file_path  = a temp file containing the markdown body
#
# We pass the body via a file (not via shell args) because summary
# bodies routinely contain newlines, quotes, and the kind of markdown
# punctuation that shell quoting handles poorly.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

SOURCE_ID="${1:?source_entry_id required as first arg}"
TITLE="${2:?title required as second arg}"
BODY_FILE="${3:?body file path required as third arg}"

if [[ ! -f "$BODY_FILE" ]]; then
    echo "body file not found: $BODY_FILE" >&2
    exit 2
fi

BODY=$(cat "$BODY_FILE")

# Step 0: validate body structure before we touch the server. A
# malformed summary (missing sections, too few retrieval phrases,
# inventing entry ids that don't exist) creates corpus noise — refusing
# to post and exiting non-zero is cheaper than letting it through and
# cleaning up later. The tick exits without recording progress, so
# the source entry stays in the backlog for the next tick.
required_sections=(
    '## Subject'
    '## Core claim'
    '## When to retrieve'
    '## Domain'
    '## Caveats'
    '## Source'
)
for section in "${required_sections[@]}"; do
    if ! grep -qF "$section" "$BODY_FILE"; then
        echo "validation: missing required section: $section" >&2
        exit 3
    fi
done

# When-to-retrieve quality floor: at least 5 distinct phrases.
# Extract the section's content (lines between "## When to retrieve"
# and the next "## ") and count comma-separated items.
retrieve_section=$(awk '
    /^## When to retrieve/      { capture=1; next }
    /^## / && capture           { exit }
    capture                     { print }
' "$BODY_FILE")
if [[ -z "$retrieve_section" ]]; then
    echo "validation: '## When to retrieve' section is empty" >&2
    exit 3
fi
# Count comma-separated phrases (with light trimming).
phrase_count=$(echo "$retrieve_section" \
    | tr ',' '\n' \
    | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' \
    | grep -cE '.{3,}')
if [[ "$phrase_count" -lt 5 ]]; then
    echo "validation: '## When to retrieve' has $phrase_count phrases; minimum 5 required (if you can't find 5, use no_action instead)" >&2
    exit 3
fi

# Verify Source section cites the actual source entry id we were
# called with. Catches the "cataloger paraphrased a different id"
# failure mode.
if ! grep -qF "[[$SOURCE_ID]]" "$BODY_FILE"; then
    echo "validation: body must cite [[$SOURCE_ID]] (the source) at least once" >&2
    exit 3
fi

# Verify every wiki-linked id in the body actually exists. Catches
# hallucinated ids (a real failure mode observed in early ticks). We
# allow [[$SOURCE_ID]] always — the source obviously exists. Other
# referenced ids must come back live from /v1/entries/<id>.
referenced_ids=$(grep -oE '\[\[(T|D|X|L|I|M|F|E)-[A-Z0-9]+\]\]' "$BODY_FILE" \
    | sed 's/^\[\[//;s/\]\]$//' \
    | sort -u)
for id in $referenced_ids; do
    [[ "$id" == "$SOURCE_ID" ]] && continue
    code=$(curl --retry 5 --retry-connrefused -sS -o /dev/null -w '%{http_code}' \
        -H "Authorization: Bearer $KB_TOKEN" \
        "$KB_URL/v1/entries/$id")
    if [[ "$code" != "200" ]]; then
        echo "validation: body cites [[$id]] but that entry does not exist (HTTP $code). do not invent or paraphrase ids — only cite real entries you've verified." >&2
        exit 3
    fi
done

# Step 1: post the librarian_meta DRAFT.
#
# `metadata` goes on the wire as a real JSON object — not a JSON
# string. The server-side type is json.RawMessage so what we send is
# what we get back. Earlier iterations of this script wrapped the
# inner object in tostring/tojson, which made the stored value an
# escaped JSON string inside a JSON string (outer quotes + every
# inner quote escaped). One bare object below = clean storage.
ENTRY_PAYLOAD=$(jq -n \
    --arg title "$TITLE" \
    --arg body "$BODY" \
    --arg role "$KB_ROLE" \
    --arg instance "$KB_INSTANCE_ID" \
    --arg source "$SOURCE_ID" \
    '{
        project_id: "omoikane",
        type: "librarian_meta",
        status: "DRAFT",
        title: $title,
        body: $body,
        body_format: "markdown",
        tags: ["librarian","cataloger","summary"],
        metadata: {
            role: $role,
            instance_id: $instance,
            kind: "cataloger_summary",
            source_entry_id: $source
        }
    }')

ENTRY_RESP=$(curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/entries" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$ENTRY_PAYLOAD")
DRAFT_ID=$(echo "$ENTRY_RESP" | jq -r .id)

if [[ -z "$DRAFT_ID" || "$DRAFT_ID" == "null" ]]; then
    echo "failed to create DRAFT — server response: $ENTRY_RESP" >&2
    exit 1
fi

# Step 2: record progress (this is what removes source from the backlog)
PROGRESS_PAYLOAD=$(jq -n \
    --arg role "$KB_ROLE" \
    --arg source "$SOURCE_ID" \
    --arg instance "$KB_INSTANCE_ID" \
    --arg draft "$DRAFT_ID" \
    --arg title "$TITLE" \
    '{role: $role, entry_id: $source, instance_id: $instance,
      action: "summarized", output_entry_id: $draft,
      notes: ("summary draft: " + $title)}')

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/progress" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PROGRESS_PAYLOAD" >/dev/null

# Step 3: heartbeat
HEARTBEAT_PAYLOAD=$(jq -n \
    --arg note "summarized $SOURCE_ID -> $DRAFT_ID" \
    '{note: $note, did_action: true}')
curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$HEARTBEAT_PAYLOAD" >/dev/null

# Confirm on stdout so the caller can chain.
jq -n --arg draft "$DRAFT_ID" --arg source "$SOURCE_ID" \
    '{draft_id: $draft, source_entry_id: $source, action: "summarized"}'
