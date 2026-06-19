#!/usr/bin/env bash
# Fetch the oldest unprocessed entry for this role from omoikane.
# Prints the full JSON response to stdout. Exit codes:
#   0 = entry returned (use it as the work unit for this tick)
#   1 = transport error
#   2 = credential / config problem
#   42 = backlog is drained (no work)
#
# Optional first arg: project_id (restricts the backlog to one project).
# Useful when running multiple cataloger instances, one per project.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

PROJECT_FILTER=""
if [[ "${1:-}" != "" ]]; then
    PROJECT_FILTER="&project_id=$1"
fi

# Emergency-stop check before doing anything else.
STATUS=$(curl --retry 5 --retry-connrefused -fsS -H "Authorization: Bearer $KB_TOKEN" \
    "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID" | jq -r .status)
if [[ "$STATUS" == "stopped" ]]; then
    echo "{\"emergency_stop\": true}" >&2
    # Still heartbeat to record liveness, then exit clean.
    curl --retry 5 --retry-connrefused -fsS -X POST -H "Authorization: Bearer $KB_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"note":"honoring emergency stop","did_action":false}' \
        "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" >/dev/null
    exit 0
fi

RESP=$(curl --retry 5 --retry-connrefused -sS -w '\n%{http_code}' -H "Authorization: Bearer $KB_TOKEN" \
    "$KB_URL/v1/librarian/backlog/next?role=$KB_ROLE${PROJECT_FILTER}")
BODY=$(echo "$RESP" | sed '$d')
CODE=$(echo "$RESP" | tail -n1)

case "$CODE" in
    200)
        echo "$BODY"
        ;;
    404)
        # backlog drained — heartbeat and exit 42 so the caller knows
        curl --retry 5 --retry-connrefused -fsS -X POST -H "Authorization: Bearer $KB_TOKEN" \
            -H "Content-Type: application/json" \
            -d '{"note":"backlog drained","did_action":false}' \
            "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" >/dev/null
        echo "{\"backlog_drained\": true}" >&2
        exit 42
        ;;
    *)
        echo "backlog_next: HTTP $CODE: $BODY" >&2
        exit 1
        ;;
esac
