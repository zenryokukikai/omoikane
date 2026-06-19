#!/usr/bin/env bash
# Upsert a UseCase. Takes a JSON body either as a single argument or via stdin.
# Body shape (slug auto-derived from name_en):
#   {"name_ja":"...","name_en":"...","description_ja":"...","description_en":"...","domain":"..."}
#
# Idempotent: re-posting the same name_en updates the existing row.
# Source is stamped to "indexer:<instance>" for audit.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

BODY="${1:-$(cat)}"
PAYLOAD=$(jq --arg src "indexer:${KB_INSTANCE_ID}" '. + {source: $src}' <<<"$BODY")

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/use_cases" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PAYLOAD"
