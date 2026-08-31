#!/usr/bin/env bash
# Post the day's daily journal to Slack via an incoming webhook.
# Called by the wrapper AFTER the journal is written, so Slack delivery is
# deterministic (not left to the LLM). Safe to run even with no webhook
# configured — it just skips.
#
# Webhook URL is a SECRET: read from .agents/.local/slack-webhook.json
#   { "webhook_url": "https://hooks.slack.com/services/..." }
# (that dir is gitignored). Falls back to $SLACK_WEBHOOK_URL.
#
# Usage: notify_slack.sh [YYYY-MM-DD]   (default: yesterday JST = the day the
#        journal covers)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.local" && pwd)"
WEBHOOK=""
if [ -f "$LOCAL_DIR/slack-webhook.json" ]; then
    WEBHOOK=$(jq -r '.webhook_url // empty' "$LOCAL_DIR/slack-webhook.json" 2>/dev/null || true)
fi
WEBHOOK="${WEBHOOK:-${SLACK_WEBHOOK_URL:-}}"
if [ -z "$WEBHOOK" ]; then
    echo "[slack] no webhook configured (.agents/.local/slack-webhook.json) — skipping"
    exit 0
fi

TARGET="${1:-$(TZ=Asia/Tokyo date -v-1d +%Y-%m-%d 2>/dev/null || TZ=Asia/Tokyo date -d 'yesterday' +%Y-%m-%d)}"

# The link in the message is clicked by a HUMAN, so it must be the public
# base URL. KB_URL is the API endpoint and in production is
# http://127.0.0.1:<port> — journal announcements really did go out to
# Slack pointing at 127.0.0.1, unopenable for everyone. Refuse to send
# rather than send a dead link; the fix is one key in kb-agent.json.
if [ -z "${KB_PUBLIC_URL:-}" ]; then
    echo "[slack] kb-agent.json has no \"kb_public_url\" — refusing to post a journal link nobody can open" >&2
    exit 2
fi

# Find the daily_journal entry for TARGET.
# The response goes through a temp FILE, never an argv: Linux caps a
# single argument at 128KB (MAX_ARG_STRLEN) and this payload is well past
# it, so passing it as `python3 - "$RESP"` died with E2BIG on the server
# while working fine on macOS, which has no such cap. Keep it a file.
RESPF=$(mktemp)
trap 'rm -f "$RESPF"' EXIT
curl --retry 5 --retry-connrefused -fsS -H "Authorization: Bearer $KB_TOKEN" \
    "$KB_URL/v1/entries?type=librarian_meta&limit=400" -o "$RESPF"

PAYLOAD=$(KB_PUBLIC_URL="$KB_PUBLIC_URL" TARGET="$TARGET" python3 - "$RESPF" <<'PY'
import os, sys, json
target = os.environ["TARGET"]; kb = os.environ["KB_PUBLIC_URL"].rstrip("/")
data = json.load(open(sys.argv[1]), strict=False)
j = None
for e in data.get("entries", []):
    m = e.get("metadata") or {}
    if not isinstance(m, dict):
        continue
    if m.get("kind") == "daily_journal" and m.get("journal_date") == target:
        j = e; break
if j is None:
    print(""); sys.exit(0)

import re
body = j.get("body") or ""

def to_mrkdwn(t):
    t = re.sub(r"\[\[[^\]]+\]\]", "", t)                              # drop [[wiki]] refs
    t = re.sub(r"\[([^\]]+)\]\((https?://[^)]+)\)", r"<\2|\1>", t)     # [text](url) -> <url|text>
    t = re.sub(r"\*\*([^*]+)\*\*", r"*\1*", t)                        # **bold** -> *bold*
    t = re.sub(r"[ \t]{2,}", " ", t)
    return t.strip()

# Slack gets a FURTHER-summarised digest, not the whole journal: the lead
# overview the journal opens with (everything before the first "## " section),
# minus the title echo. Falls back to the first section if there's no lead.
head = re.split(r"\n#{1,6}\s", body, maxsplit=1)[0]
lead_lines = [l for l in head.split("\n")
              if l.strip()
              and not re.match(r"#{1,6}\s", l)
              and "daily journal" not in l.lower()]
lead = "\n".join(lead_lines).strip()
if not lead:                                  # no lead → take first section's prose
    secs = re.split(r"\n#{1,6}\s", body)
    lead = (secs[1] if len(secs) > 1 else body)[:600]
lead = to_mrkdwn(lead)
if len(lead) > 1400:
    lead = lead[:1400].rsplit("。", 1)[0] + "。…"

# Highlights — the actual topics of the day, to make a reader want to click.
def section(name):
    m = re.search(r"\n##\s*" + re.escape(name) + r"\b(.*?)(?=\n##\s|\Z)", body, re.S)
    return m.group(1) if m else ""

ext_heads = re.findall(r"^\s*[-*]\s*\*\*(.+?)\*\*", section("外部の注目"), re.M)[:3]
projects = [p.strip() for p in re.findall(r"^###\s*(.+)$", section("内部の新知見"), re.M)]
bullets = ["• " + to_mrkdwn(h) for h in ext_heads]
if projects:
    bullets.append("• 内部: " + " / ".join(projects[:3]) + " が前進")

url = f"{kb}/entries/{j['id']}"
parts = [f"📝 *omoikane 日次ジャーナル {target}*", "", lead]
if bullets:
    parts += ["", "*今日のハイライト*", "\n".join(bullets)]
parts += ["", f"📖 続きは暦のジャーナルで → <{url}|全文を読む>"]
text = "\n".join(parts)
print(json.dumps({
    "text": text,
    "username": "暦",
    "icon_emoji": ":sunrise_over_mountains:",
    "unfurl_links": False,
}, ensure_ascii=False))
PY
)

if [ -z "$PAYLOAD" ]; then
    echo "[slack] no daily_journal for $TARGET — skipping"
    exit 0
fi

CODE=$(curl --retry 5 --retry-connrefused -sS -o /dev/null -w "%{http_code}" \
    -X POST "$WEBHOOK" -H "Content-Type: application/json" -d "$PAYLOAD")
echo "[slack] posted journal $TARGET → HTTP $CODE"
