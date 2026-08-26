#!/usr/bin/env bash
# omoikane-gate smoke check (issue #104 slice G3, E2E prep).
#
# Run AFTER omoikane-gate is up and the two confirmation-time values
# (OPENCRAB_GATE_SOCKET, GATEWAY_TOKEN) are filled in. It performs three
# non-destructive, idempotent checks and fails loudly on any of them:
#
#   1. Roster:    GET /v1/gateway/librarians with the gateway token,
#                 print the connectable roster.
#   2. Socket:    the core socket exists at OPENCRAB_GATE_SOCKET (where
#                 that path is visible to this script — see note below).
#   3. Log lines: the gate logs show a successful connect
#                 ("gate instance connected" / "event stream connected").
#
# Nothing here writes state, hits no mutating endpoint, and is safe to
# run repeatedly. Secrets come from the environment; none are hardcoded.
#
# Environment is the contract:
#   KB_BASE_URL           omoikane API base (e.g. http://localhost:8080)
#   GATEWAY_TOKEN         gateway-scoped user-less bearer token
#   OPENCRAB_GATE_SOCKET  core socket path (check 2 skips if unset/not
#                         visible from where this runs — the socket lives
#                         inside the gate container / shared volume)
#   GATE_LOG_CMD          command that prints the gate logs for check 3.
#                         Default:
#                           docker compose -f deploy/omoikane-gate.compose.yml \
#                             logs --no-log-prefix omoikane-gate
#
# Usage:
#   export KB_BASE_URL=http://localhost:8080
#   export GATEWAY_TOKEN=...            # user-less, scope gateway
#   export OPENCRAB_GATE_SOCKET=/run/opencrab/core.sock
#   ./scripts/gateway-smoke.sh

set -euo pipefail

fail() { printf 'SMOKE FAIL: %s\n' "$*" >&2; exit 1; }
ok()   { printf 'ok: %s\n' "$*"; }

command -v curl >/dev/null 2>&1 || fail "curl not found"
command -v jq   >/dev/null 2>&1 || fail "jq not found"

: "${KB_BASE_URL:?set KB_BASE_URL to the omoikane API base}"
: "${GATEWAY_TOKEN:?set GATEWAY_TOKEN to the gateway-scoped user-less token}"

BASE="${KB_BASE_URL%/}"

# --- Check 1: librarian roster ---------------------------------------
printf '== check 1: GET /v1/gateway/librarians ==\n'
body="$(mktemp)"; trap 'rm -f "$body"' EXIT
code="$(curl -sS -o "$body" -w '%{http_code}' \
  -H "Authorization: Bearer ${GATEWAY_TOKEN}" \
  "${BASE}/v1/gateway/librarians")" \
  || fail "roster request failed (transport)"

[ "$code" = "200" ] || fail "roster returned HTTP $code, body: $(cat "$body")"
jq -e '.librarians' "$body" >/dev/null 2>&1 \
  || fail "roster body has no .librarians array: $(cat "$body")"

total="$(jq '.librarians | length' "$body")"
connectable="$(jq '[.librarians[] | select(.gate_instance_id != "" and .gate_instance_id != null)] | length' "$body")"
printf 'roster: %s librarian(s), %s connectable (non-empty gate_instance_id)\n' "$total" "$connectable"
jq -r '.librarians[] | "  - user=\(.user_id) agent=\(.agent_id) name=\(.name) instance=\(.gate_instance_id // "<none>")"' "$body"
ok "roster reachable"
if [ "$connectable" = "0" ]; then
  printf 'note: 0 connectable rows — no instance registered yet (upstream opencrab#763 / provisioning). The gate has nothing to connect until this is > 0.\n'
fi

# --- Check 2: core socket present ------------------------------------
printf '== check 2: core socket present ==\n'
if [ -z "${OPENCRAB_GATE_SOCKET:-}" ]; then
  printf 'skip: OPENCRAB_GATE_SOCKET unset in this shell (socket lives inside the gate container / shared volume; run this check where that path is visible).\n'
elif [ -S "$OPENCRAB_GATE_SOCKET" ]; then
  ok "socket exists at $OPENCRAB_GATE_SOCKET"
elif [ -e "$OPENCRAB_GATE_SOCKET" ]; then
  fail "$OPENCRAB_GATE_SOCKET exists but is not a socket"
else
  fail "no socket at $OPENCRAB_GATE_SOCKET (core not listening, or not visible from here)"
fi

# --- Check 3: connect log lines --------------------------------------
printf '== check 3: gate connect log lines ==\n'
LOG_CMD="${GATE_LOG_CMD:-docker compose -f deploy/omoikane-gate.compose.yml logs --no-log-prefix omoikane-gate}"
logs="$(mktemp)"; trap 'rm -f "$body" "$logs"' EXIT
if ! eval "$LOG_CMD" >"$logs" 2>/dev/null; then
  fail "could not read gate logs via GATE_LOG_CMD ($LOG_CMD) — set GATE_LOG_CMD to a command that prints them"
fi

if grep -q 'event stream connected' "$logs"; then
  ok "SSE stream connected (event stream connected)"
else
  fail "no 'event stream connected' line — the gate has not reached omoikane's SSE stream"
fi

if grep -q 'gate instance connected' "$logs"; then
  n="$(grep -c 'gate instance connected' "$logs")"
  ok "hello succeeded for $n instance connect(s) (gate instance connected)"
else
  if [ "${connectable:-0}" = "0" ]; then
    printf 'note: no "gate instance connected" line yet, but 0 connectable rows — expected until an instance is registered.\n'
  else
    fail "no 'gate instance connected' line despite $connectable connectable row(s) — hello to the core has not succeeded"
  fi
fi

printf '\nSMOKE OK\n'
