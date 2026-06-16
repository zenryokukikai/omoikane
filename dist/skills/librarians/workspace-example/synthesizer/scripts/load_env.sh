#!/usr/bin/env bash
# Source this from the other scripts to populate env from kb-agent.json.
# Read the credential file each invocation (per-invocation secrets,
# survives rotation without restarts).
set -euo pipefail

CRED_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.local" && pwd)/kb-agent.json"
if [[ ! -f "$CRED_FILE" ]]; then
    echo "credential file not found: $CRED_FILE" >&2
    exit 2
fi

export KB_URL=$(jq -r .kb_core_url "$CRED_FILE")
export KB_TOKEN=$(jq -r .api_key "$CRED_FILE")
export KB_INSTANCE_ID=$(jq -r .instance_id "$CRED_FILE")
export KB_ROLE=$(jq -r '.librarian_role // "indexer"' "$CRED_FILE")

if [[ -z "$KB_URL" || -z "$KB_TOKEN" || -z "$KB_INSTANCE_ID" ]]; then
    echo "credential incomplete: KB_URL/KB_TOKEN/KB_INSTANCE_ID required" >&2
    exit 2
fi
