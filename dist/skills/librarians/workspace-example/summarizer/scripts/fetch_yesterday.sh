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
#                 "unparsed_created_at": N, "oldest_updated_at": "...",
#                 "list_total_first": N, "list_total_last": N,
#                 "incomplete_because": [ "..." ] }
#   }
#
# READ `scan.complete` BEFORE TRUSTING AN EMPTY RESULT. false means the
# day was never provably covered — a cut-short scan, a page the server
# could not account for, or timestamps we failed to parse — so "nothing
# happened that day" is NOT established; `incomplete_because` says which,
# and the script also exits 3. Empty counts with scan.complete=true is a
# genuinely quiet day.
#
# WHAT THIS DELIBERATELY DOES NOT SEE: the list endpoint excludes
# SUPERSEDED / ARCHIVED / DUPLICATE, and we do not pass
# include_superseded. So an entry created on the target day but folded
# away since will not appear. That is intended — the journal reports the
# knowledge that is still standing, not what was later retracted (43 of
# 18,199 prod entries were in that state on 2026-08-31). If you ever need
# the day exactly as it was lived, add include_superseded=true here and
# say so in the journal.
#
# Prior daily journals (kind=daily_journal) are EXCLUDED so the journal
# never summarises itself.
#
# Exit codes: 0 ok · 1 unexpected (fetch failed after retries, bad
# SUMMARIZER_MAX_PAGES) · 2 env/credentials · 3 scan incomplete.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/load_env.sh"

TARGET="${1:-$(TZ=Asia/Tokyo date -v-1d +%Y-%m-%d 2>/dev/null || TZ=Asia/Tokyo date -d 'yesterday' +%Y-%m-%d)}"

# Page budget: 40 pages x 500 = 20k entries. That is NOT a promise of N
# days' reach — the list is ordered by updated_at, so one bulk re-touch
# (an indexer pass over old entries) can fill the window with entries far
# newer than they look. The budget only stops a bug from paging forever;
# when it is the thing that stopped the scan you get
# stop_reason=page_limit and exit 3, never a quiet zero. Raise it then.
MAX_PAGES="${SUMMARIZER_MAX_PAGES:-40}"

TARGET="$TARGET" MAX_PAGES="$MAX_PAGES" python3 - <<'PY'
import datetime, json, os, re, sys, time, urllib.request

KB_URL = os.environ["KB_URL"].rstrip("/")
KB_TOKEN = os.environ["KB_TOKEN"]
target = os.environ["TARGET"]
MAX_PAGES = int(os.environ["MAX_PAGES"])
# Page size we ASK for; the server decides what it actually returns (it
# clamps ?limit at 500, and a larger value gets a self-contradictory
# response), so paging advances by its next_offset, not by this number.
PAGE_SIZE = 500

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
offset = 0
oldest_updated_at = None
totals = []            # pagination.total as seen on each page
stop_reason = "page_limit"  # only survives if we exhaust MAX_PAGES

while pages_fetched < MAX_PAGES:
    data = fetch_page(offset)
    pages_fetched += 1
    batch = data.get("entries") or []
    pagination = data.get("pagination")
    if not isinstance(pagination, dict):
        pagination = {}
    if isinstance(pagination.get("total"), int):
        totals.append(pagination["total"])

    for e in batch:
        if e.get("id") not in seen:
            seen.add(e.get("id"))
            entries.append(e)
    if batch:
        oldest_updated_at = batch[-1].get("updated_at") or oldest_updated_at

    # Advance by the server's OWN next_offset, never by our page size.
    # The server clamps ?limit (and elsewhere in this API a too-large
    # limit is silently rounded DOWN), so "offset += PAGE_SIZE" would
    # step over everything the clamp left behind and still report a
    # clean scan. Following next_offset keeps the page-size contract in
    # one place — the server's.
    nxt = pagination.get("next_offset")
    offset = nxt if isinstance(nxt, int) and nxt > offset else offset + len(batch)

    # END OF LIST — only on PROOF: the key exists and says has_more=false.
    # A missing/!= false has_more is not evidence of anything; treating a
    # falsy `.get()` as "the list ended" is exactly how one page of 500
    # gets to call itself a full scan.
    if pagination.get("has_more") is False:
        stop_reason = "end_of_list"
        break

    if not batch:
        # The server claims more (or said nothing) yet handed back an
        # empty page: we cannot make progress and we cannot conclude.
        stop_reason = "empty_page"
        break

    tail = parse_ts(batch[-1].get("updated_at") or "")
    if tail is not None and tail < day_start:
        stop_reason = "covered_target_day"
        break
    # tail unparseable -> keep paging rather than stop on a guess.

ext, knowledge = [], []
activity = {}
unparsed_created_at = 0
KNOWLEDGE_TYPES = {"trap", "lesson", "decision", "incident", "design"}

for e in entries:
    day = jst_date(e.get("created_at", ""))
    if day is None:
        # NEVER swallow this. An unparsable timestamp is indistinguishable
        # from "not the target day" once it is dropped, and that silence
        # is precisely what hid #147 (9-digit fractions) and then its
        # 5-digit sibling. Count it; it makes the scan incomplete below.
        unparsed_created_at += 1
        continue
    if day != target:
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

# ---------------------------------------------------------------------
# Completeness is derived from EVIDENCE, not from the label we broke on.
# The question is only ever: "did the scan reach back past the target
# day's first instant?" Two ways to have proof:
#   - covered_target_day: a page tail older than day_start. Direct proof.
#   - end_of_list: the whole list was walked. That only proves coverage
#     if what we saw actually reaches past day_start (or the list was
#     empty). A list that "ended" while everything in it is NEWER than
#     the target day proves nothing about the day — the far more likely
#     reading is a lying/absent pagination, so say so instead of
#     reporting a confident zero. (A genuinely empty history before the
#     target day lands here too: loud and wrong beats silent and wrong.)
# And the filter itself must have worked: an unparsable created_at is
# dropped from every bucket, which is how #147 (nanoseconds) and its
# 5-digit sibling both hid. Any of those and the scan is not complete.
# ---------------------------------------------------------------------
incomplete_because = []
if stop_reason == "covered_target_day":
    covered = True
elif stop_reason == "end_of_list":
    oldest_ts = parse_ts(oldest_updated_at or "")
    covered = not entries or (oldest_ts is not None and oldest_ts < day_start)
    if not covered:
        incomplete_because.append(
            "list claimed to end at %s, which is not older than the target day — "
            "coverage unproven" % oldest_updated_at)
else:
    covered = False
    if stop_reason == "page_limit":
        incomplete_because.append(
            "page budget (%d) exhausted before reaching the target day" % MAX_PAGES)
    else:  # empty_page
        incomplete_because.append(
            "server returned an empty page without saying the list had ended")
if unparsed_created_at:
    incomplete_because.append(
        "%d entries had a created_at this script could not parse and were "
        "dropped from every bucket" % unparsed_created_at)

complete = covered and not unparsed_created_at

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
                "unparsed_created_at": unparsed_created_at,
                "oldest_updated_at": oldest_updated_at,
                # total as the server reported it on the first and last
                # page: differing values mean the list moved under the
                # scan (writes land while we page).
                "list_total_first": totals[0] if totals else None,
                "list_total_last": totals[-1] if totals else None,
                "incomplete_because": incomplete_because}}
print(json.dumps(out, ensure_ascii=False))

if not complete:
    sys.stderr.write(
        "INCOMPLETE SCAN for %s (stopped: %s after %d pages / %d entries, "
        "oldest updated_at %s):\n%s\n"
        "The counts above are a floor, not the day. Fix the cause (raise "
        "SUMMARIZER_MAX_PAGES if it was the page budget) and re-run; do NOT "
        "write a journal from this.\n"
        % (target, stop_reason, pages_fetched, len(entries), oldest_updated_at,
           "".join("  - %s\n" % r for r in incomplete_because).rstrip("\n")))
    sys.exit(3)
PY
