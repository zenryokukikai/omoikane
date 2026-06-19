#!/usr/bin/env bash
# Return the next piece of REAL curator work — not the next arbitrary
# entry. Curator is signal-driven (per the canonical bundle): it acts
# on detective's relation proposals and on review-queue health cases,
# NOT on every entry in the corpus. Feeding it the full entry backlog
# wastes one LLM tick per non-proposal entry just to record no_action.
#
# Priority:
#   1. The oldest unresolved relation_proposal (detective's output that
#      curator has not yet recorded progress on).
#   2. Otherwise, the oldest review-queue entry curator has not yet
#      processed.
#
# Output (stdout) is a JSON object tagging which kind of work it is:
#   {"work":"relation_proposal","entry":{...}}    # the proposal entry
#   {"work":"review_queue","entry":{...}}         # the flagged entry
# Exit codes:
#   0  = work returned
#   42 = no outstanding work (curator is caught up — the normal idle state)
#   1/2 = transport / credential error
#
# Emergency stop + heartbeat-on-empty are handled here (mirrors
# backlog_next.sh) so the SKILL doesn't have to.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

AUTH=(-H "Authorization: Bearer $KB_TOKEN")

# --- emergency stop --------------------------------------------------
STATUS=$(curl --retry 5 --retry-connrefused -fsS "${AUTH[@]}" \
    "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID" | jq -r .status)
if [[ "$STATUS" == "stopped" ]]; then
    curl --retry 5 --retry-connrefused -fsS -X POST "${AUTH[@]}" -H "Content-Type: application/json" \
        -d '{"note":"honoring emergency stop","did_action":false}' \
        "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" >/dev/null
    echo '{"emergency_stop":true}' >&2
    exit 42
fi

# --- set of entry_ids curator has already processed ------------------
# librarian_progress is curator's "done" ledger; entry_id there is the
# proposal id (or review-queue entry id) it acted on.
PROCESSED=$(curl --retry 5 --retry-connrefused -fsS "${AUTH[@]}" \
    "$KB_URL/v1/librarian/progress?role=$KB_ROLE&instance_id=$KB_INSTANCE_ID&limit=500" \
    | jq -r '[.progress[].entry_id] | unique')

# --- 1. oldest unresolved relation_proposal --------------------------
# List librarian_meta DRAFTs, keep kind=relation_proposal, drop any
# already in PROCESSED, take the oldest by created_at.
PROPOSALS=$(curl --retry 5 --retry-connrefused -fsS "${AUTH[@]}" \
    "$KB_URL/v1/entries?type=librarian_meta&status=DRAFT&limit=200")
NEXT_PROP=$(echo "$PROPOSALS" | jq -c --argjson done "$PROCESSED" '
    [ .entries[]
      | select((.metadata // {}).kind == "relation_proposal")
      | select((.id as $id | $done | index($id)) | not) ]
    | sort_by(.created_at) | .[0] // empty')

if [[ -n "$NEXT_PROP" && "$NEXT_PROP" != "null" ]]; then
    jq -n --argjson e "$NEXT_PROP" '{work:"relation_proposal", entry:$e}'
    exit 0
fi

# --- 2. oldest unprocessed review-queue entry ------------------------
RQ=$(curl --retry 5 --retry-connrefused -fsS "${AUTH[@]}" "$KB_URL/v1/review-queue")
NEXT_RQ=$(echo "$RQ" | jq -c --argjson done "$PROCESSED" '
    [ .queue[] | select((.ID as $id | $done | index($id)) | not) ] | .[0] // empty')

if [[ -n "$NEXT_RQ" && "$NEXT_RQ" != "null" ]]; then
    # Fetch the full entry so the SKILL has its body to judge.
    RQ_ID=$(echo "$NEXT_RQ" | jq -r .ID)
    ENTRY=$(curl --retry 5 --retry-connrefused -fsS "${AUTH[@]}" "$KB_URL/v1/entries/$RQ_ID")
    jq -n --argjson e "$ENTRY" '{work:"review_queue", entry:$e}'
    exit 0
fi

# --- nothing outstanding: heartbeat and report idle ------------------
curl --retry 5 --retry-connrefused -fsS -X POST "${AUTH[@]}" -H "Content-Type: application/json" \
    -d '{"note":"no outstanding proposals or review-queue work","did_action":false}' \
    "$KB_URL/v1/librarian/instances/$KB_INSTANCE_ID/heartbeat" >/dev/null
echo '{"caught_up":true}' >&2
exit 42
