#!/usr/bin/env bash
# Regression tests for fetch_yesterday.sh, run against fake_kb.py.
#
# Every case here is a way the script could report a confident, WRONG
# zero for a day that had 200 entries — which is what actually happened
# in production (#148: four journals said "nothing happened"). The rule
# these tests enforce is the COVERAGE CONTRACT in fetch_yesterday.sh:
# get the day right, or say scan.complete=false and exit 3. Never a
# quiet zero — and never a false alarm on a day that IS fully covered.
#
# Usage: bash tests/test_fetch_yesterday.sh
# Needs: bash, python3, jq (load_env.sh reads the credential file with jq).
# Exit: 0 all pass · 1 failures · 2 could not run (missing jq).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$(dirname "$HERE")"
TARGET_DAY=2026-08-20    # a day in the middle of the fixture
OLDEST_DAY=2026-08-10    # the fixture's first day: nothing is older

if ! command -v jq >/dev/null; then
    echo "CANNOT RUN: jq is not installed (load_env.sh needs it)" >&2
    exit 2
fi
echo "python: $(python3 -V 2>&1) — $(command -v python3)"

WORK=$(mktemp -d)
WS="$WORK/ws/.agents"
mkdir -p "$WS/.local" "$WS/skills/omoikane-summarizer/scripts"
cp "$SCRIPTS/fetch_yesterday.sh" "$SCRIPTS/load_env.sh" "$WS/skills/omoikane-summarizer/scripts/"
SUT="$WS/skills/omoikane-summarizer/scripts/fetch_yesterday.sh"

PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
cat > "$WS/.local/kb-agent.json" <<EOF
{"kb_core_url":"http://127.0.0.1:$PORT","api_key":"test-token",
 "instance_id":"summarizer-test","librarian_role":"summarizer"}
EOF

SERVER_PID=""
stop_server() {
    [ -n "$SERVER_PID" ] || return 0
    kill "$SERVER_PID" 2>/dev/null
    wait "$SERVER_PID" 2>/dev/null   # reap before bash reports "Terminated"
    SERVER_PID=""
}
trap 'stop_server; rm -rf "$WORK"' EXIT

start_server() {  # <mode>
    python3 "$HERE/fake_kb.py" "$PORT" "$1" & SERVER_PID=$!
    for _ in $(seq 50); do
        if python3 -c "
import sys,urllib.request
try: urllib.request.urlopen('http://127.0.0.1:$PORT/v1/entries?limit=1', timeout=1)
except Exception: sys.exit(1)" 2>/dev/null; then return 0; fi
        sleep 0.1
    done
    echo "fake_kb.py ($1) did not come up on $PORT" >&2; exit 1
}

FAILED=0
CASE=""
OUT="$WORK/out.json"
ERR="$WORK/err.txt"
RC=0
DAY="$TARGET_DAY"

run_case() {  # <name> <mode> <date> [env assignments...]
    CASE="$1"; local mode="$2"; DAY="$3"; shift 3
    start_server "$mode"
    env "$@" bash "$SUT" "$DAY" >"$OUT" 2>"$ERR"
    RC=$?
    stop_server
}

check() {  # <python expression over o> <what it means>
    if ! python3 -c '
import json, sys
o = json.load(open(sys.argv[1]))
sys.exit(0 if eval(sys.argv[2], {"o": o}) else 1)' "$OUT" "$1" 2>/dev/null; then
        echo "  FAIL [$CASE] $2"
        echo "        expected: $1"
        echo "        got scan: $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("scan"))' "$OUT" 2>/dev/null || cat "$OUT")"
        FAILED=1
    fi
}

check_rc() {  # <expected rc>
    if [ "$RC" != "$1" ]; then
        echo "  FAIL [$CASE] exit code $RC, expected $1"
        [ -s "$ERR" ] && sed 's/^/        /' "$ERR"
        FAILED=1
    fi
}

# The fixture answers for itself — no hard-coded counts to drift.
day_counts() {
    local exp
    # DONTWRITEBYTECODE: expected.py imports fake_kb; without this the
    # suite leaves a __pycache__ dir behind in the repo.
    exp=$(cd "$HERE" && PYTHONDONTWRITEBYTECODE=1 python3 expected.py "$DAY")
    check "o['counts']['external_findings'] == $(jq .external_findings <<<"$exp")" "external_findings for $DAY"
    check "o['counts']['new_knowledge'] == $(jq .new_knowledge <<<"$exp")" "new_knowledge for $DAY"
    check "o['counts']['librarian_meta'] == $(jq .librarian_meta <<<"$exp")" "librarian_meta for $DAY (journal excluded)"
    # Identity, not just arithmetic: the entry at 00:00:00 JST of THIS
    # day must be in the result. Per-day mixes differ, so a scan that is
    # off by one day fails the counts above; this pins the boundary.
    check "'$(jq -r .first_ext_id <<<"$exp")' in [f['id'] for f in o['external_findings']]" \
          "first instant of the JST day is included"
}

# Same rule for the tree: the fixture answers, nothing is hard-coded.
tree_counts() {
    local exp
    exp=$(cd "$HERE" && python3 expected.py "$DAY")
    check "o['tree_snapshot']['complete'] is True" "tree_snapshot.complete"
    check "o['tree_snapshot']['total_use_cases'] == $(jq .total_use_cases <<<"$exp")" \
          "paged past the 200 ?limit cap — the whole tree, not the first page"
    check "o['tree_snapshot']['top_level_count'] == $(jq .top_level_count <<<"$exp")" \
          "top-level headcount from ?level=top"
    check "o['counts']['use_cases_created'] == $(jq .use_cases_created <<<"$exp")" \
          "UseCases created on $DAY"
    check "o['counts']['use_cases_touched'] == $(jq .use_cases_touched <<<"$exp")" \
          "UseCases touched (not created) on $DAY"
    check "o['counts']['empty_leaves'] == $(jq .empty_leaves <<<"$exp")" \
          "empty leaves exclude metas (child_count>0)"
}

echo "== honest server: the whole day, provably covered"
run_case "honest" honest "$TARGET_DAY"
check_rc 0
check "o['scan']['complete'] is True" "scan.complete"
check "o['scan']['covered_by'] == 'tail_passed_day'" "covered by proof P1"
check "o['scan']['unparsed_created_at'] == 0" "every created_at parsed (#147 + its 5-digit sibling)"
day_counts
# The tree snapshot is fetched in the same run; the fixture has 250
# UseCases against a ?limit the server caps at 200, so a client that
# takes one page reports 200 and a short "touched" list.
tree_counts

echo "== oldest day in the KB (P1 can never happen there)"
# Regression: requiring a page tail older than the day made the KB's
# first day permanently incomplete — a day we have ALL the data for
# could never be journalled.
run_case "oldest day" honest "$OLDEST_DAY"
check_rc 0
check "o['scan']['complete'] is True" "scan.complete"
check "o['scan']['covered_by'] == 'whole_list_walked'" "covered by proof P2"
check "o['scan']['rows_accounted'] >= o['scan']['list_total_last']" "rows accounted for reach the reported total"
day_counts

echo "== server clamps the page size (limit ignored, 137 returned)"
run_case "clamp" clamp "$TARGET_DAY"
check_rc 0
check "o['scan']['complete'] is True" "scan.complete"
day_counts

echo "== next_offset jumps past rows the server never sent"
# Regression: following next_offset blindly leaves a hole in the middle
# and the scan still calls itself complete.
run_case "skipping next_offset" skipping_next_offset "$TARGET_DAY"
check_rc 0
check "o['scan']['next_offset_skips_ignored'] > 0" "the skip was noticed, not followed"
check "o['scan']['complete'] is True" "still a complete scan — we walked the rows ourselves"
day_counts

echo "== response has no pagination object at all"
run_case "nopagination" nopagination "$TARGET_DAY"
check_rc 0
check "o['scan']['stop_reason'] == 'covered_target_day'" "must not claim end_of_list without proof"
check "o['scan']['complete'] is True" "scan.complete"
day_counts

echo "== read view lost: total says 18k, list comes back empty"
# Regression: an ACL/space regression empties the list with no error.
# "Nothing here" must never be reported as a quiet day.
run_case "lost view" lost_view "$TARGET_DAY"
check_rc 3
check "o['scan']['complete'] is False" "an empty list under a non-zero total is not a quiet day"
check "'never arrived' in ' '.join(o['scan']['incomplete_because'])" "names the rows that never arrived"
check "o['scan']['rows_accounted'] < o['scan']['list_total_last']" "the row count is what caught it"

echo "== entirely empty list (total=0)"
run_case "empty kb" empty_kb "$TARGET_DAY"
check_rc 3
check "o['scan']['complete'] is False" "an empty everything is loud, not a quiet day"

echo "== server hands back an empty page while claiming more"
run_case "empty page" empty_after_first "$TARGET_DAY"
check_rc 3
check "o['scan']['complete'] is False" "cannot conclude from a page that never arrived"
check "o['scan']['stop_reason'] == 'empty_page'" "stop_reason"
check "len(o['scan']['incomplete_because']) > 0" "says why"

echo "== list 'ends' before reaching the target day"
run_case "short list" short_list "$TARGET_DAY"
check_rc 3
check "o['scan']['complete'] is False" "end_of_list is not coverage unless the rows add up"
check "o['scan']['stop_reason'] == 'end_of_list'" "stop_reason"
check "o['counts']['external_findings'] == 0" "the zero is real here — but it must be a LOUD zero"

echo "== page budget exhausted"
run_case "page limit" honest "$TARGET_DAY" SUMMARIZER_MAX_PAGES=1
check_rc 3
check "o['scan']['complete'] is False" "scan.complete"
check "o['scan']['stop_reason'] == 'page_limit'" "stop_reason"
check "o['scan']['pages_fetched'] == 1" "budget respected"

echo "== created_at we cannot parse"
run_case "unparsable" unparsable "$TARGET_DAY"
check_rc 3
check "o['scan']['unparsed_created_at'] == 3" "counted, not swallowed"
check "o['scan']['complete'] is False" "a dropped entry means the day is not established"

if [ "$FAILED" = 0 ]; then echo "ALL PASS"; else echo "FAILURES"; fi
exit "$FAILED"
