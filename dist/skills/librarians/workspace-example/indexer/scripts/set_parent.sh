#!/usr/bin/env bash
# set_parent.sh — repoint a UseCase under a parent (tidy mode).
#
# Usage: set_parent.sh <child_ref> <parent_ref>
#   <child_ref>  use_case id (U-XXXXXX) or slug
#   <parent_ref> use_case id or slug — pass empty string "" (or omit) to un-root.
#
# Implementation: POST /v1/use_cases/{ref}/parent {parent_id}. This dedicated
# endpoint can ALSO un-root (empty parent_id), which the old upsert-based
# approach could NOT — upsert deliberately preserves the existing parent when
# parent_id is empty, so "un-root via upsert" silently did nothing.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

CHILD="${1:?child use_case ref (id or slug) required}"
PARENT="${2:-}"   # empty = un-root to top level

PAYLOAD=$(jq -n --arg parent "$PARENT" '{parent_id:$parent}')

curl --retry 5 --retry-connrefused -fsS -X POST "$KB_URL/v1/use_cases/$CHILD/parent" \
    -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
    -d "$PAYLOAD" | jq '{id, slug, parent_id}'
