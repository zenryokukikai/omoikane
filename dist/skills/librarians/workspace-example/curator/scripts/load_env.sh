#!/usr/bin/env bash
# Source this from the other scripts to populate env from kb-agent.json.
# We deliberately read the credential file each invocation rather than
# baking it into a shell rc — this keeps secrets per-invocation and
# survives credential rotation without restarting anything.
set -euo pipefail

CRED_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.local" && pwd)/kb-agent.json"
if [[ ! -f "$CRED_FILE" ]]; then
    echo "credential file not found: $CRED_FILE" >&2
    exit 2
fi

export KB_URL=$(jq -r .kb_core_url "$CRED_FILE")
export KB_TOKEN=$(jq -r .api_key "$CRED_FILE")
export KB_INSTANCE_ID=$(jq -r .instance_id "$CRED_FILE")
export KB_ROLE=$(jq -r '.librarian_role // "cataloger"' "$CRED_FILE")

# Base URL for links a HUMAN will click (Slack messages, journal bodies).
# Deliberately NOT derived from KB_URL: KB_URL is the API endpoint and is
# routinely an internal/loopback address (an IPv6 workaround pointed it at
# http://127.0.0.1:<port>), and links built from it went out to Slack as
# 127.0.0.1 — nobody could open them. Left EMPTY unless the credential
# file carries "kb_public_url"; scripts that build human-facing links must
# handle empty loudly rather than silently fall back to KB_URL.
export KB_PUBLIC_URL=$(jq -r '.kb_public_url // ""' "$CRED_FILE")

if [[ -z "$KB_URL" || -z "$KB_TOKEN" || -z "$KB_INSTANCE_ID" ]]; then
    echo "credential incomplete: KB_URL/KB_TOKEN/KB_INSTANCE_ID required" >&2
    exit 2
fi
