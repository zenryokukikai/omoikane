#!/usr/bin/env bash
# Announce ONE freshly-posted scout finding to Slack via incoming webhook.
# Called by post_finding.sh AFTER the entry is created; never fails the
# posting pipeline (best-effort, always exits 0). Safe with no webhook
# configured — it just skips.
#
# Webhook URL is a SECRET: .agents/.local/slack-webhook.json
#   { "webhook_url": "https://hooks.slack.com/services/..." }   (gitignored)
# Falls back to $SLACK_WEBHOOK_URL.
#
# Usage: notify_slack_finding.sh <entry_id> <title> <source_url> <relevance> <body_file> [ja_summary]
# ja_summary (日本語 2–3 文) is preferred as the message body; the English
# "Why it matters" excerpt from body_file is only the fallback.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh" 2>/dev/null || true

ENTRY_ID="${1:-}"; TITLE="${2:-}"; URL="${3:-}"; RELEVANCE="${4:-}"; BODY_FILE="${5:-}"; JA_SUMMARY="${6:-}"
[ -n "$ENTRY_ID" ] && [ -n "$TITLE" ] || { echo "[slack] missing args — skipping"; exit 0; }

LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.local" 2>/dev/null && pwd || true)"
WEBHOOK=""
if [ -n "$LOCAL_DIR" ] && [ -f "$LOCAL_DIR/slack-webhook.json" ]; then
    WEBHOOK=$(jq -r '.webhook_url // empty' "$LOCAL_DIR/slack-webhook.json" 2>/dev/null || true)
fi
WEBHOOK="${WEBHOOK:-${SLACK_WEBHOOK_URL:-}}"
if [ -z "$WEBHOOK" ]; then
    echo "[slack] no webhook configured — skipping"
    exit 0
fi

# KB_PUBLIC_URL, never KB_URL: the reader clicks this link, and KB_URL is
# the API endpoint, which in production is http://127.0.0.1:<port>. When
# the credential file carries no "kb_public_url" we still send the
# announcement (the source link is absolute and useful on its own) but
# drop the KB entry link rather than emit an unopenable one.
if [ -z "${KB_PUBLIC_URL:-}" ]; then
    echo "[slack] no kb_public_url in kb-agent.json — announcing without the KB entry link" >&2
fi
PAYLOAD=$(TITLE="$TITLE" URL="$URL" ENTRY_ID="$ENTRY_ID" RELEVANCE="$RELEVANCE" \
          KB_PUBLIC_URL="${KB_PUBLIC_URL:-}" BODY_FILE="$BODY_FILE" \
          JA_SUMMARY="$JA_SUMMARY" python3 - <<'PY'
import os, json, re
title = os.environ["TITLE"]; url = os.environ["URL"]
kb = (os.environ.get("KB_PUBLIC_URL") or "").rstrip("/")
entry = os.environ["ENTRY_ID"]; rel = os.environ.get("RELEVANCE", "")

# Preferred: the Japanese summary passed by the scout at post time.
why = (os.environ.get("JA_SUMMARY") or "").strip()
if not why:
    # Fallback: English "Why it matters" excerpt from the entry body.
    body = ""
    bf = os.environ.get("BODY_FILE") or ""
    if bf and os.path.isfile(bf):
        body = open(bf, encoding="utf-8", errors="replace").read()
    m = re.search(r"##\s*(?:なぜ重要か|Why it matters)[^\n]*\n(.*?)(?=\n##\s|\Z)", body, re.S)
    why = (m.group(1).strip() if m else "")
why = why.replace("**", "*")
why = re.sub(r"[ \t]{2,}", " ", why)
if len(why) > 600:
    why = why[:600].rsplit("。", 1)[0] + "。…"

lines = [f"🔭 *scout が新しいネタを拾いました*",
         f"*<{url}|{title}>*"]
if why:
    lines += ["", why]
if kb:
    lines += ["", f"📖 KB エントリ → <{kb}/entries/{entry}|{entry}>"
                  + (f"  (relevance {rel})" if rel else "")]
elif rel:
    lines += ["", f"📖 KB エントリ {entry}  (relevance {rel})"]
print(json.dumps({
    "text": "\n".join(lines),
    "username": "scout",
    "icon_emoji": ":telescope:",
    "unfurl_links": False,
}, ensure_ascii=False))
PY
) || { echo "[slack] payload build failed — skipping"; exit 0; }

CODE=$(curl --retry 3 --retry-connrefused -sS -o /dev/null -w "%{http_code}" \
    -X POST "$WEBHOOK" -H "Content-Type: application/json" -d "$PAYLOAD" || echo "err")
echo "[slack] notified finding $ENTRY_ID → HTTP $CODE"
exit 0
