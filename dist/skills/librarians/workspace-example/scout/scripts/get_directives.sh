#!/usr/bin/env bash
# Fetch the operator's ACTIVE watch-topic directives for the scout.
# Empty array = no directives = the patrol is exactly the normal one.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"
curl --retry 3 -sS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/librarian/directives?role=scout&active=1" \
  | jq -c '[.directives[] | {id, text, by: .created_by_name}]'
