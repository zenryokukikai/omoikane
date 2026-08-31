#!/usr/bin/env bash
# Gather everything that happened in omoikane on the PREVIOUS calendar
# day (JST), grouped for the summarizer to turn into a daily journal.
#
# "Yesterday" is the JST calendar day before today. Entry created_at is
# stored UTC; we convert to JST and keep entries whose JST date == that
# day. Optional arg overrides the target date (YYYY-MM-DD, JST) — that
# is the backfill path, and it must work for a day ANY number of days
# back, not just yesterday.
#
# Emits one JSON object on stdout:
#   {
#     "date": "YYYY-MM-DD",
#     "external_findings": [ {id,title,url,body} ... ],   // scout
#     "new_knowledge":    [ {id,type,title,body} ... ],   // trap/lesson/decision/incident/design
#     "librarian_activity": { "cataloger_summary": N, "relation_proposal": M,
#                             "curator_resolution": K, ... },  // librarian_meta kinds
#     "counts": { "external_findings": .., "new_knowledge": .. },
#     "scan":   { "complete": true|false, "stop_reason": "...",
#                 "pages_fetched": N, "entries_scanned": M,
#                 "oldest_updated_at": "..." }
#   }
#
# READ `scan.complete` BEFORE TRUSTING AN EMPTY RESULT. false means the
# scan was cut short, so "nothing happened that day" is NOT established;
# the script also exits 3 in that case. Empty counts with
# scan.complete=true is a genuinely quiet day.
#
# Prior daily journals (kind=daily_journal) are EXCLUDED so the journal
# never summarises itself.
#
# Exit codes: 0 ok · 2 env/credentials · 3 scan incomplete (see above).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

TARGET="${1:-$(TZ=Asia/Tokyo date -v-1d +%Y-%m-%d 2>/dev/null || TZ=Asia/Tokyo date -d 'yesterday' +%Y-%m-%d)}"

# Page budget. 500 (the server's per-page cap) x 40 pages = 20k entries,
# ~60 days at the current ~300/day. Raise it for a deeper backfill; it
# exists only so a bug can't page forever.
MAX_PAGES="${SUMMARIZER_MAX_PAGES:-40}"

TARGET="$TARGET" MAX_PAGES="$MAX_PAGES" python3 - <<'PY'
import datetime, json, os, re, sys, time, urllib.request

KB_URL = os.environ["KB_URL"].rstrip("/")
KB_TOKEN = os.environ["KB_TOKEN"]
target = os.environ["TARGET"]
MAX_PAGES = int(os.environ["MAX_PAGES"])
PAGE_SIZE = 500  # server clamps ?limit at 500 (internal/store/entries.go)

JST = datetime.timezone(datetime.timedelta(hours=9))
# First instant of the target JST day, as an absolute point in time.
day_start = datetime.datetime.strptime(target, "%Y-%m-%d").replace(tzinfo=JST)


def parse_ts(iso):
    # created_at/updated_at like "2026-05-31T19:55:02Z" or with offset;
    # naive values are treated as UTC.
    #
    # kb-server stamps NANOSECOND precision (9 digits:
    # "2026-08-31T05:42:36.718262515Z"). Python's fromisoformat accepts at
    # most 6 fractional digits before 3.11, and this box runs 3.10 — so
    # every single entry raised ValueError and was silently skipped as
    # "not yesterday". The journal then reported "no new entries" on days
    # with 300+ of them (2026-08-31). Trim to microseconds before parsing.
    #
    # Trimming alone is not enough: Go's RFC3339Nano also DROPS trailing
    # zeros, so a stamp can be SHORTER than 6 digits
    # ("2026-08-25T15:36:22.41465Z") — and 3.10 accepts only 3 or 6, so
    # those raised ValueError as well (2 such entries in ~12k of prod
    # data, i.e. rare enough to hide, frequent enough to lose entries).
    # Normalise the fraction to exactly 6 digits: pad short, trim long.
    s = iso.replace("Z", "+00:00")
    s = re.sub(r"\.(\d+)", lambda m: "." + (m.group(1) + "000000")[:6], s)
    try:
        dt = datetime.datetime.fromisoformat(s)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    return dt


def jst_date(iso):
    dt = parse_ts(iso)
    return dt.astimezone(JST).strftime("%Y-%m-%d") if dt else None


def fetch_page(offset):
    """One page of the entry list, with retries for transient failures."""
    url = "%s/v1/entries?limit=%d&offset=%d" % (KB_URL, PAGE_SIZE, offset)
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + KB_TOKEN})
    last_err = None
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                return json.load(resp)
        except Exception as err:  # network hiccup / 5xx / restart mid-deploy
            last_err = err
            time.sleep(2 ** attempt)
    raise SystemExit("fetch failed at offset %d: %s" % (offset, last_err))


# ---------------------------------------------------------------------
# Page the list until the target day is covered.
#
# WHY WE MAY STOP EARLY, AND WHY IT LOSES NOTHING:
#   The list is ordered updated_at DESC (internal/store/entries.go), and
#   updated_at >= created_at always holds. So once a page ENDS at an
#   updated_at earlier than the target day's first instant, every entry
#   after it in the list has an even smaller updated_at, hence a smaller
#   created_at — none of them can have been created on the target day.
#   There is nothing left to find, and we stop.
#
#   This is why the old code was wrong in a way that could not be fixed
#   by "fetch more": it took ONE page of 500 and filtered client-side, so
#   any day older than the newest 500 entries silently produced 0 hits.
#   Do not go back to a bigger fixed window — the day must be *covered*,
#   and the coverage must be provable (that is what scan.complete says).
#
# Offsets can shift under concurrent writes, which may repeat an entry
# across pages; dedupe by id. (A shift can in principle also skip one,
# which offset paging cannot prevent — it is bounded by how many entries
# are written during the scan, seconds' worth.)
# ---------------------------------------------------------------------
entries, seen = [], set()
pages_fetched = 0
oldest_updated_at = None
stop_reason = "page_limit"  # only survives if we exhaust MAX_PAGES

while pages_fetched < MAX_PAGES:
    data = fetch_page(pages_fetched * PAGE_SIZE)
    pages_fetched += 1
    batch = data.get("entries") or []
    pagination = data.get("pagination") or {}

    for e in batch:
        if e.get("id") not in seen:
            seen.add(e.get("id"))
            entries.append(e)
    if batch:
        oldest_updated_at = batch[-1].get("updated_at") or oldest_updated_at

    if not batch or not pagination.get("has_more"):
        stop_reason = "end_of_list"  # scanned the whole list; nothing beyond
        break

    tail = parse_ts(batch[-1].get("updated_at") or "")
    if tail is not None and tail < day_start:
        stop_reason = "covered_target_day"
        break
    # tail unparseable -> keep paging rather than stop on a guess.

complete = stop_reason != "page_limit"

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
        # Carry more body so the journal can state the *effect/magnitude*
        # (numbers, conditions) — those usually sit deeper in the abstract.
        ext.append({"id": e["id"], "title": e.get("title", ""),
                    "url": (meta.get("source_url") if isinstance(meta, dict) else "") or "",
                    "body": (e.get("body") or "")[:1500]})
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
                  "librarian_meta": sum(activity.values())},
       # The caller must be able to tell "the day was quiet" from "we
       # never got far enough back to see the day".
       "scan": {"complete": complete, "stop_reason": stop_reason,
                "pages_fetched": pages_fetched, "entries_scanned": len(entries),
                "oldest_updated_at": oldest_updated_at}}
print(json.dumps(out, ensure_ascii=False))

if not complete:
    sys.stderr.write(
        "INCOMPLETE SCAN for %s: stopped after %d pages (%d entries, oldest "
        "updated_at %s) without reaching the target day. The counts above are "
        "a floor, not the day. Raise SUMMARIZER_MAX_PAGES and re-run; do NOT "
        "write a journal from this.\n"
        % (target, pages_fetched, len(entries), oldest_updated_at))
    sys.exit(3)
PY
