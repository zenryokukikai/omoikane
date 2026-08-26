#!/usr/bin/env bash
# gateway-qc-e2e.sh — full-minimal /talk E2E bring-up (issue #104 QC).
#
# Runs on a fresh machine on which the PLATFORM side is already up:
# the opencrab runtime + gate admin plane (default http://127.0.0.1:18700)
# and the core's Unix socket. This script brings up the OMOIKANE side
# from zero and drives one human→librarian round trip through the gate:
#
#    1. build   kb-server + omoikane-gate
#    2. start   kb-server on a FRESH sqlite DB (migrations auto-run)
#    3. mint    an admin token           (kb-server admin-token CLI)
#    4. create  the QC owner user + API token (same CLI — see NOTE 1)
#    5. save    the personal librarian via POST /my/librarian
#               (provisions the agent on opencrab AND registers the
#               gate instance via GATE_ADMIN_URL)
#    6. mint    the USER-LESS gateway token        (see NOTE 2)
#    7. start   omoikane-gate against the core socket
#    8. open    a /talk thread (server-side triggers the binding PUT)
#    9. post    a human message, poll for the assistant reply
#   10. summary; all processes this script started are killed on exit
#
# Environment (all optional — defaults fit the QC layout):
#   KB_PORT               kb-server port                (default 18080)
#   QC_DIR                work dir for db/logs/tokens   (default mktemp -d)
#   KB_DB_PATH            sqlite path — must NOT exist  (default $QC_DIR/kb.db)
#   OPENCRAB_URL          opencrab runtime base    (default http://127.0.0.1:18700)
#   GATE_ADMIN_URL        gate admin plane base    (default = OPENCRAB_URL)
#   GATE_OPERATOR_TOKEN   operator bearer for the admin plane (default empty)
#   OPENCRAB_OWNER_ID     trusted owner id written into the agent's trust
#                         row (default qc-operator; must match what the
#                         platform side expects)
#   OPENCRAB_GATE_SOCKET  the core's UDS path      (default /tmp/opencrab/gate.sock)
#   QC_LIBRARIAN_NAME     librarian display name   (default きりんQC)
#   QC_REPLY_TIMEOUT      seconds to wait for the assistant reply (default 120)
#
# NOTE 1 (QC user): there is no non-interactive HTTP path to create a
#   human user (signup is Google OAuth / member-invite redemption in a
#   browser). The official non-interactive path IS the admin CLI:
#   `kb-server admin-token -user … -role member` creates the user row
#   (with its personal space) and mints its API token in one step.
#
# NOTE 2 (gateway token): the gateway token must be USER-LESS (user_id
#   NULL, scope "gateway" — docs/gateway-runbook.md prereq 2), but the
#   admin-token CLI cannot mint one: `-user ""` fails in CreateUser
#   (empty id = invalid input). No official non-interactive path exists
#   today, so this script does the smallest honest workaround: generate
#   the plain token locally and INSERT the api_tokens row directly
#   (token_hash = SHA-256(plain), user_id NULL, scopes
#   read,write,gateway) with the sqlite3 CLI. Same row CreateToken
#   would write. Follow-up: teach admin-token a --userless flag.
#
# Dependencies: bash, curl, jq, go, sqlite3, shasum|sha256sum, openssl.
# No secrets are hardcoded; everything minted lands under $QC_DIR
# (mode 700). Nothing here touches a production DB — kb-server starts
# on a fresh file and refuses to reuse an existing one.

set -euo pipefail

# ---- config ----------------------------------------------------------
KB_PORT="${KB_PORT:-18080}"
QC_DIR="${QC_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/omoikane-qc-e2e.XXXXXX")}"
KB_DB_PATH="${KB_DB_PATH:-$QC_DIR/kb.db}"
OPENCRAB_URL="${OPENCRAB_URL:-http://127.0.0.1:18700}"
GATE_ADMIN_URL="${GATE_ADMIN_URL:-$OPENCRAB_URL}"
GATE_OPERATOR_TOKEN="${GATE_OPERATOR_TOKEN:-}"
OPENCRAB_OWNER_ID="${OPENCRAB_OWNER_ID:-qc-operator}"
OPENCRAB_GATE_SOCKET="${OPENCRAB_GATE_SOCKET:-/tmp/opencrab/gate.sock}"
QC_LIBRARIAN_NAME="${QC_LIBRARIAN_NAME:-きりんQC}"
QC_REPLY_TIMEOUT="${QC_REPLY_TIMEOUT:-120}"

KB_BASE="http://127.0.0.1:${KB_PORT}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
QC_USER="qc-owner"

mkdir -p "$QC_DIR" && chmod 700 "$QC_DIR"

# ---- helpers ---------------------------------------------------------
STEP=0
declare -a RESULTS=()

step()  { STEP=$((STEP + 1)); printf '\n== step %d: %s ==\n' "$STEP" "$*"; }
pass()  { RESULTS+=("PASS  step $STEP: $*"); printf 'PASS: %s\n' "$*"; }
fail()  {
  RESULTS+=("FAIL  step $STEP: $*")
  printf 'FAIL: %s\n' "$*" >&2
  summary
  exit 1
}

summary() {
  printf '\n==================== QC E2E SUMMARY ====================\n'
  # ${arr[@]+…}: empty-array-safe under set -u on macOS bash 3.2.
  printf '%s\n' ${RESULTS[@]+"${RESULTS[@]}"}
  printf 'work dir: %s (db, logs, minted tokens)\n' "$QC_DIR"
  printf '========================================================\n'
}

# diag: platform-side probes printed on failures that depend on the
# platform being up — diagnose, do not just die.
diag_platform() {
  printf -- '---- diagnostics -------------------------------------\n'
  printf 'opencrab runtime %s: HTTP %s\n' "$OPENCRAB_URL" \
    "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$OPENCRAB_URL/api/agents" || echo unreachable)"
  local code
  local -a auth=()
  if [ -n "$GATE_OPERATOR_TOKEN" ]; then
    auth=(-H "Authorization: Bearer $GATE_OPERATOR_TOKEN")
  fi
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    ${auth[@]+"${auth[@]}"} \
    "$GATE_ADMIN_URL/api/gate-instances/00000000-0000-0000-0000-000000000000" || echo unreachable)"
  printf 'gate admin plane %s: HTTP %s' "$GATE_ADMIN_URL" "$code"
  if [ "$code" = "401" ]; then
    printf '  (401: GATE_OPERATOR_TOKEN wrong or missing)'
  fi
  printf '\n'
  if [ -S "$OPENCRAB_GATE_SOCKET" ]; then
    printf 'core socket %s: present\n' "$OPENCRAB_GATE_SOCKET"
  else
    printf 'core socket %s: MISSING (is the core running?)\n' "$OPENCRAB_GATE_SOCKET"
  fi
  printf 'kb-server log tail:\n';        tail -n 5 "$QC_DIR/kb-server.log" 2>/dev/null || true
  printf 'omoikane-gate log tail:\n';    tail -n 5 "$QC_DIR/omoikane-gate.log" 2>/dev/null || true
  printf -- '------------------------------------------------------\n'
}

sha256_hex() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1
  else sha256sum | cut -d' ' -f1; fi
}

# admin_token USER NAME ROLE SCOPES → prints the plain token
admin_token() {
  KB_DB_PATH="$KB_DB_PATH" "$QC_DIR/kb-server" admin-token \
    -user "$1" -name "$2" -role "$3" -scopes "$4" | tail -n 1
}

KB_PID="" GATE_PID=""
cleanup() {
  # Only processes THIS script started.
  [ -n "$GATE_PID" ] && kill "$GATE_PID" 2>/dev/null || true
  [ -n "$KB_PID" ]   && kill "$KB_PID"   2>/dev/null || true
}
trap cleanup EXIT

for dep in curl jq go sqlite3 openssl; do
  command -v "$dep" >/dev/null 2>&1 || { echo "missing dependency: $dep" >&2; exit 1; }
done

# ---- 1. build --------------------------------------------------------
step "build kb-server + omoikane-gate"
( cd "$REPO_ROOT" &&
  go build -tags sqlite_fts5 -o "$QC_DIR/kb-server" ./cmd/kb-server &&
  go build -o "$QC_DIR/omoikane-gate" ./cmd/omoikane-gate ) \
  || fail "go build failed"
pass "binaries in $QC_DIR"

# ---- 2. start kb-server on a fresh DB --------------------------------
step "start kb-server (fresh DB $KB_DB_PATH, port $KB_PORT)"
if [ -e "$KB_DB_PATH" ]; then
  fail "KB_DB_PATH already exists: $KB_DB_PATH — refusing to reuse a DB; point KB_DB_PATH somewhere fresh or remove it yourself"
fi
KB_DB_PATH="$KB_DB_PATH" \
KB_HTTP_ADDR="127.0.0.1:${KB_PORT}" \
KB_OAUTH_REDIRECT_BASE="$KB_BASE" \
OPENCRAB_URL="$OPENCRAB_URL" \
OPENCRAB_OWNER_ID="$OPENCRAB_OWNER_ID" \
GATE_ADMIN_URL="$GATE_ADMIN_URL" \
GATE_OPERATOR_TOKEN="$GATE_OPERATOR_TOKEN" \
  "$QC_DIR/kb-server" >"$QC_DIR/kb-server.log" 2>&1 &
KB_PID=$!
for _ in $(seq 1 50); do
  if curl -sf -o /dev/null --max-time 2 "$KB_BASE/v1/health"; then break; fi
  kill -0 "$KB_PID" 2>/dev/null || { tail -n 20 "$QC_DIR/kb-server.log"; fail "kb-server exited during startup"; }
  sleep 0.2
done
curl -sf -o /dev/null "$KB_BASE/v1/health" || fail "kb-server did not answer /v1/health"
pass "kb-server up (pid $KB_PID)"

# ---- 3. admin token --------------------------------------------------
step "mint admin token (kb-server admin-token)"
ADMIN_TOKEN="$(admin_token qc-admin qc-e2e-admin admin read,write,admin)" \
  || fail "admin-token CLI failed"
[ -n "$ADMIN_TOKEN" ] || fail "admin token came back empty"
printf '%s\n' "$ADMIN_TOKEN" > "$QC_DIR/admin-token.txt"
pass "admin token minted (qc-admin)"

# ---- 4. QC owner user + token ---------------------------------------
step "create QC user + API token (admin-token CLI creates the user row — NOTE 1)"
OWNER_TOKEN="$(admin_token "$QC_USER" qc-e2e-owner member read,write)" \
  || fail "owner token mint failed"
[ -n "$OWNER_TOKEN" ] || fail "owner token came back empty"
printf '%s\n' "$OWNER_TOKEN" > "$QC_DIR/owner-token.txt"
curl -sf -H "Authorization: Bearer $OWNER_TOKEN" "$KB_BASE/v1/auth/me" >/dev/null \
  || fail "owner token does not authenticate against /v1/auth/me"
pass "user $QC_USER + token ready"

# ---- 5. save the personal librarian ---------------------------------
step "save personal librarian '$QC_LIBRARIAN_NAME' (POST /my/librarian → opencrab provision + gate instance PUT)"
code="$(curl -s -o "$QC_DIR/librarian-save.out" -w '%{http_code}' \
  -H "Authorization: Bearer $OWNER_TOKEN" \
  --data-urlencode "name=$QC_LIBRARIAN_NAME" \
  --data-urlencode "persona=QC end-to-end test librarian. Reply briefly." \
  "$KB_BASE/my/librarian")" || code=000
if [ "$code" != "303" ]; then
  printf 'save answered HTTP %s; server error banner:\n' "$code"
  # The error page is HTML; surface just the banner line (full body
  # stays in $QC_DIR/librarian-save.out).
  grep -o '<div class="banner">[^<]*' "$QC_DIR/librarian-save.out" \
    | sed 's/<div class="banner">//' || head -c 400 "$QC_DIR/librarian-save.out"
  diag_platform
  fail "librarian save failed (needs opencrab at $OPENCRAB_URL and gate admin at $GATE_ADMIN_URL)"
fi
pass "librarian saved"

# ---- 6. user-less gateway token (NOTE 2) -----------------------------
step "mint user-less gateway token (direct api_tokens INSERT — NOTE 2)"
GATEWAY_TOKEN="$(openssl rand -hex 32)"
GW_HASH="$(printf '%s' "$GATEWAY_TOKEN" | sha256_hex)"
sqlite3 "$KB_DB_PATH" \
  "INSERT INTO api_tokens(token_hash, user_id, name, scopes, token_type)
   VALUES ('$GW_HASH', NULL, 'gateway-qc-e2e', 'read,write,gateway', 'api');" \
  || fail "sqlite insert of the gateway token failed"
printf '%s\n' "$GATEWAY_TOKEN" > "$QC_DIR/gateway-token.txt"

ROSTER="$(curl -sf -H "Authorization: Bearer $GATEWAY_TOKEN" "$KB_BASE/v1/gateway/librarians")" \
  || fail "gateway token rejected by GET /v1/gateway/librarians"
INSTANCE_ID="$(printf '%s' "$ROSTER" | jq -r --arg u "$QC_USER" \
  '.librarians[] | select(.user_id == $u) | .gate_instance_id')"
if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "null" ]; then
  printf 'roster: %s\n' "$ROSTER"
  diag_platform
  fail "librarian has no gate_instance_id — instance registration on $GATE_ADMIN_URL did not happen (subject_id resolvable on the runtime? operator token valid?)"
fi
pass "gateway token works; instance $INSTANCE_ID registered"

# ---- 7. start omoikane-gate -----------------------------------------
step "start omoikane-gate (socket $OPENCRAB_GATE_SOCKET)"
if [ ! -S "$OPENCRAB_GATE_SOCKET" ]; then
  diag_platform
  fail "core socket missing at $OPENCRAB_GATE_SOCKET — the platform core must be running first"
fi
OPENCRAB_GATE_SOCKET="$OPENCRAB_GATE_SOCKET" \
KB_BASE_URL="$KB_BASE" \
GATEWAY_TOKEN="$GATEWAY_TOKEN" \
GATE_STATIC_INSTANCES="" \
GATE_DISCOVERY_INTERVAL=5s \
  "$QC_DIR/omoikane-gate" >"$QC_DIR/omoikane-gate.log" 2>&1 &
GATE_PID=$!
CONNECTED=""
for _ in $(seq 1 50); do
  kill -0 "$GATE_PID" 2>/dev/null || { tail -n 20 "$QC_DIR/omoikane-gate.log"; fail "omoikane-gate exited during startup"; }
  if grep -q "gate instance connected" "$QC_DIR/omoikane-gate.log"; then CONNECTED=1; break; fi
  sleep 0.3
done
if [ -z "$CONNECTED" ]; then
  diag_platform
  fail "gate never logged 'gate instance connected' (hello with the core failed?)"
fi
pass "omoikane-gate connected (pid $GATE_PID)"

# ---- 8. open a /talk thread -----------------------------------------
step "open /talk thread (triggers the binding PUT server-side)"
THREAD_ID="$(curl -sf -H "Authorization: Bearer $OWNER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"intent":"talk","title":"QC gateway E2E"}' \
  "$KB_BASE/v1/librarian/threads" | jq -r .thread_id)"
[ -n "$THREAD_ID" ] && [ "$THREAD_ID" != "null" ] || fail "thread creation failed"
if grep -q "talk gate binding" "$QC_DIR/kb-server.log"; then
  printf 'note: kb-server logged a gate-binding warning:\n'
  grep "talk gate binding" "$QC_DIR/kb-server.log" | tail -n 2
fi
pass "thread $THREAD_ID open"

# ---- 9. human message → assistant reply ------------------------------
step "post human message, wait up to ${QC_REPLY_TIMEOUT}s for the assistant reply"
MSG_ID="$(curl -sf -H "Authorization: Bearer $OWNER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"thread_id\":\"$THREAD_ID\",\"author_role\":\"human\",\"intent\":\"question\",\"content\":\"こんにちは。QC E2E テストです。一言だけ返事をください。\"}" \
  "$KB_BASE/v1/librarian/chat" | jq -r .id)"
[ -n "$MSG_ID" ] && [ "$MSG_ID" != "null" ] || fail "human message post failed"
printf 'human message %s posted; polling…\n' "$MSG_ID"

REPLY=""
DEADLINE=$(( $(date +%s) + QC_REPLY_TIMEOUT ))
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  REPLY="$(curl -sf -H "Authorization: Bearer $OWNER_TOKEN" \
    "$KB_BASE/v1/librarian/threads/$THREAD_ID/messages?since=$MSG_ID&limit=20" \
    | jq -r '[.messages[] | select(.author_role == "assistant")][0].content // empty')" || REPLY=""
  if [ -n "$REPLY" ]; then break; fi
  sleep 3
done
if [ -z "$REPLY" ]; then
  diag_platform
  fail "no assistant reply within ${QC_REPLY_TIMEOUT}s (full loop: SSE→said→core→agent→say→chat POST — see the two log tails above)"
fi
printf 'assistant replied: %s\n' "$REPLY"
pass "full round trip complete"

# ---- 10. summary -----------------------------------------------------
step "summary"
pass "QC E2E complete"
summary
