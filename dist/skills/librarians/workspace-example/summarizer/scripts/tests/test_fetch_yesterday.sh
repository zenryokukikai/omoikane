#!/usr/bin/env bash
# Regression tests for fetch_yesterday.sh, run against fake_kb.py.
#
# Every case here is a way the script could report a confident, WRONG
# zero for a day that had 200 entries — which is what actually happened
# in production (#148: four journals said "nothing happened"). The rule
# these tests enforce: get the day right, or say scan.complete=false and
# exit 3. Never a quiet zero.
#
# Usage: bash tests/test_fetch_yesterday.sh
# Needs: bash, python3, jq (load_env.sh reads the credential file with jq).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$(dirname "$HERE")"
TARGET_DAY=2026-08-20   # fake_kb.py gives every day 200 entries
EXPECT_EXT=10
EXPECT_KNOW=20
EXPECT_META=169         # 170 librarian_meta minus the day's own journal

command -v jq >/dev/null || { echo "SKIP: jq not installed"; exit 0; }

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

run_case() {  # <name> <mode> [env assignments...]
    CASE="$1"; local mode="$2"; shift 2
    start_server "$mode"
    env "$@" bash "$SUT" "$TARGET_DAY" >"$OUT" 2>"$ERR"
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

day_counts() {  # the three buckets must match the fixture exactly
    check "o['counts']['external_findings'] == $EXPECT_EXT" "external_findings"
    check "o['counts']['new_knowledge'] == $EXPECT_KNOW" "new_knowledge"
    check "o['counts']['librarian_meta'] == $EXPECT_META" "librarian_meta (journal excluded)"
}

echo "== honest server: the whole day, provably covered"
run_case "honest" honest
check_rc 0
check "o['scan']['complete'] is True" "scan.complete"
check "o['scan']['stop_reason'] == 'covered_target_day'" "stopped on proof, not on a guess"
check "o['scan']['unparsed_created_at'] == 0" "every created_at parsed (#147 + its 5-digit sibling)"
day_counts
# JST boundary: the 00:00:00 JST entry of the target day belongs to it,
# and the 23:59:59.999999999 JST entry of the day before does not (it
# would show up as an extra librarian_meta above).
check "'T-02000' in [f['id'] for f in o['external_findings']]" "first instant of the JST day is included"

echo "== server clamps the page size (limit ignored, 137 returned)"
# Regression: advancing by our OWN page size steps over the remainder and
# still reports a clean scan. Must follow pagination.next_offset.
run_case "clamp" clamp
check_rc 0
check "o['scan']['complete'] is True" "scan.complete"
day_counts

echo "== response has no pagination object at all"
# Regression: `not pagination.get('has_more')` read a missing key as
# "the list ended" and declared a full scan after one page.
run_case "nopagination" nopagination
check_rc 0
check "o['scan']['stop_reason'] == 'covered_target_day'" "must not claim end_of_list without proof"
check "o['scan']['complete'] is True" "scan.complete"
day_counts

echo "== server hands back an empty page while claiming more"
run_case "empty page" empty_after_first
check_rc 3
check "o['scan']['complete'] is False" "cannot conclude from a page that never arrived"
check "o['scan']['stop_reason'] == 'empty_page'" "stop_reason"
check "len(o['scan']['incomplete_because']) > 0" "says why"

echo "== list 'ends' before reaching the target day"
run_case "short list" short_list
check_rc 3
check "o['scan']['complete'] is False" "end_of_list is not coverage unless it reaches past the day"
check "o['scan']['stop_reason'] == 'end_of_list'" "stop_reason"
check "o['counts']['external_findings'] == 0" "the zero is real here — but it must be a LOUD zero"

echo "== page budget exhausted"
run_case "page limit" honest SUMMARIZER_MAX_PAGES=1
check_rc 3
check "o['scan']['complete'] is False" "scan.complete"
check "o['scan']['stop_reason'] == 'page_limit'" "stop_reason"
check "o['scan']['pages_fetched'] == 1" "budget respected"

echo "== created_at we cannot parse"
# Regression: parse failures used to be indistinguishable from "not this
# day" — the exact hiding place of #147 and of the 5-digit stamps.
run_case "unparsable" unparsable
check_rc 3
check "o['scan']['unparsed_created_at'] == 3" "counted, not swallowed"
check "o['scan']['complete'] is False" "a dropped entry means the day is not established"

if [ "$FAILED" = 0 ]; then echo "ALL PASS"; else echo "FAILURES"; fi
exit "$FAILED"
