#!/usr/bin/env bash
# Gather everything that happened in omoikane on the PREVIOUS calendar
# day (JST), grouped for the summarizer to turn into a daily journal.
#
# "Yesterday" is the JST calendar day before today. Entry created_at is
# stored UTC; we convert to JST and keep entries whose JST date == that
# day. Optional arg overrides the target date (YYYY-MM-DD, JST).
#
# Emits one JSON object on stdout:
#   {
#     "date": "YYYY-MM-DD",
#     "external_findings": [ {id,title,url,body} ... ],   // scout
#     "new_knowledge":    [ {id,type,title,body} ... ],   // trap/lesson/decision/incident/design
#     "librarian_activity": { "cataloger_summary": N, "relation_proposal": M,
#                             "curator_resolution": K, ... },  // librarian_meta kinds
#     "counts": { "external_findings": .., "new_knowledge": .. }
#   }
# Prior daily journals (kind=daily_journal) are EXCLUDED so the journal
# never summarises itself.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

TARGET="${1:-$(TZ=Asia/Tokyo date -v-1d +%Y-%m-%d 2>/dev/null || TZ=Asia/Tokyo date -d 'yesterday' +%Y-%m-%d)}"

# Pull recent entries (all types, all live statuses; list excludes
# SUPERSEDED/ARCHIVED/DUPLICATE). 500 comfortably covers a day.
RESP_FILE=$(mktemp); trap 'rm -f "$RESP_FILE"' EXIT
curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
    "$KB_URL/v1/entries?limit=500" -o "$RESP_FILE"

# NOTE: read the entries JSON from a FILE, not stdin — the python
# program itself comes via the heredoc, which occupies stdin.
TARGET="$TARGET" RESP_FILE="$RESP_FILE" python3 - <<'PY'
import os, json, datetime
target = os.environ["TARGET"]
data = json.load(open(os.environ["RESP_FILE"]))
entries = data.get("entries", [])

def jst_date(iso):
    # created_at like "2026-05-31T19:55:02Z" or with offset; treat as UTC.
    s = iso.replace("Z", "+00:00")
    try:
        dt = datetime.datetime.fromisoformat(s)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    jst = dt.astimezone(datetime.timezone(datetime.timedelta(hours=9)))
    return jst.strftime("%Y-%m-%d")

ext, knowledge = [], []
activity = {}
KNOWLEDGE_TYPES = {"trap", "lesson", "decision", "incident", "design"}

for e in entries:
    if jst_date(e.get("created_at", "")) != target:
        continue
    meta = e.get("metadata") or {}
    kind = meta.get("kind") if isinstance(meta, dict) else None
    et = e.get("type")
    if et == "external_finding":
        ext.append({"id": e["id"], "title": e.get("title", ""),
                    "url": (meta.get("source_url") if isinstance(meta, dict) else "") or "",
                    "body": (e.get("body") or "")[:600]})
    elif et in KNOWLEDGE_TYPES:
        knowledge.append({"id": e["id"], "type": et, "title": e.get("title", ""),
                          "project_id": e.get("project_id", "") or "",
                          "body": (e.get("body") or e.get("symptom") or "")[:400]})
    elif et == "librarian_meta":
        if kind == "daily_journal":
            continue  # never summarise our own journals
        activity[kind or "other"] = activity.get(kind or "other", 0) + 1

out = {"date": target,
       "external_findings": ext,
       "new_knowledge": knowledge,
       "librarian_activity": activity,
       "counts": {"external_findings": len(ext), "new_knowledge": len(knowledge),
                  "librarian_meta": sum(activity.values())}}
print(json.dumps(out, ensure_ascii=False))
PY
