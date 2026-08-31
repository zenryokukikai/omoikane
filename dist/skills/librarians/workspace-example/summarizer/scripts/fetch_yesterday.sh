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
#                 "covered_by": "tail_passed_day"|"whole_list_walked"|null,
#                 "pages_fetched": N, "entries_scanned": M,
#                 "rows_accounted": N, "unparsed_created_at": N,
#                 "next_offset_skips_ignored": N, "oldest_updated_at": "...",
#                 "list_total_first": N, "list_total_last": N,
#                 "incomplete_because": [ "..." ] }
#   }
#
# READ `scan.complete` BEFORE TRUSTING AN EMPTY RESULT. false means the
# day was never provably covered, so "nothing happened that day" is NOT
# established; `incomplete_because` says what was missing and the script
# exits 3. Empty counts with scan.complete=true is a genuinely quiet day.
# The proof rules live in ONE place — see COVERAGE CONTRACT below.
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

# Page budget: 120 pages x 500 = 60k rows. Measured 2026-08-31 in prod:
# walking the ENTIRE list is 37 pages (18,187 rows) — ~16 s on the box
# itself (kb-core is on loopback there; minutes if you run it from off
# the box). The list grows ~300/day, so a budget of 40 was already down
# to three pages of headroom and would have started failing old-day
# backfills within days. This is NOT a promise of N days' reach either: the list
# is ordered by updated_at, so one bulk re-touch (an indexer pass over
# old entries) fills the window with rows far newer than they look. The
# budget only stops a bug from paging forever; when it is what stopped
# the scan you get stop_reason=page_limit and exit 3 — never a quiet
# zero. Raise it then.
MAX_PAGES="${SUMMARIZER_MAX_PAGES:-120}"

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
    # Name ourselves. The daily run talks to a loopback kb-core, but a
    # copy of this workspace pointed at the public host would meet an
    # edge that can judge on User-Agent — and a 403 there would surface
    # as "fetch failed", far from its cause. Costs nothing to avoid.
    req = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + KB_TOKEN,
        "User-Agent": "omoikane-summarizer/1.0",
    })
    last_err = None
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                return json.load(resp)
        except Exception as err:  # network hiccup / 5xx / restart mid-deploy
            last_err = err
            time.sleep(2 ** attempt)
    raise SystemExit("fetch failed at offset %d: %s" % (offset, last_err))


# =====================================================================
# COVERAGE CONTRACT — the ONE place this rule is written down. The
# header, the completeness block below, the stderr message and SKILL.md
# all point here and state no rule of their own; change it here.
#
# The list is ordered updated_at DESC (internal/store/entries.go) and
# updated_at >= created_at always holds. The target day is COVERED when
# one of these is PROVEN, and it is never assumed:
#
#   P1  A page ENDED at an updated_at earlier than the target day's
#       first instant. Everything further down the list has a smaller
#       updated_at, hence a smaller created_at, so nothing created on
#       the target day can still be ahead of us. Stop; nothing is lost.
#
#   P2  The whole list was walked: the server said has_more=false AND we
#       accounted for at least `total` rows. P2 is what covers the
#       OLDEST day in the KB, where P1 can never happen — there is
#       nothing older for a page to end on.
#
# Two accounting rules keep P2 honest, since "we walked it all" is worth
# exactly as much as the row count behind it:
#
#   A1  Never advance past what we were handed: offset grows by
#       len(batch). A next_offset LARGER than that is the server telling
#       us to step over rows it never sent — refusing is what keeps a
#       hole out of the middle of a scan that would still call itself
#       complete. (A smaller next_offset is followed: it can only
#       re-read rows we already have, and the dedupe absorbs that. This
#       is also what survives a clamped ?limit — we follow the rows, not
#       our own idea of a page size.)
#
#   A2  A total > 0 with nothing to show for it is not a quiet day, it
#       is a lost view. An ACL/space regression empties this list with
#       no error at all — the same silent shape as the bug that made
#       four journals report "nothing happened" (#148).
#
# The day's filter must also have worked: an unparsable created_at is
# dropped from every bucket, which is where #147 (9-digit fractions) and
# its 5-digit sibling both hid. Any of those => not complete.
#
# Never "fix" a short scan by asking for a bigger window. The day must
# be covered and the coverage must be provable: scan.complete is that
# proof, exit 3 is its absence.
#
# Offsets shift under concurrent UPDATES — the order is updated_at, so
# an indexer pass, a curator resolution or an enrich re-touches old
# entries and moves them to the front. Dedupe by id absorbs the repeats.
# =====================================================================
entries, seen = [], set()
pages_fetched = 0
offset = 0                  # rows accounted for so far (A1)
oldest_updated_at = None
totals = []                 # pagination.total as reported on each page
tail_passed_day = False     # P1
walked_whole_list = False   # P2
skips_ignored = 0           # times a next_offset tried to jump a gap (A1)
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

    handed_us = offset + len(batch)          # A1
    nxt = pagination.get("next_offset")
    if isinstance(nxt, int) and nxt > handed_us:
        skips_ignored += 1                   # would leave a hole; don't follow
    offset = nxt if isinstance(nxt, int) and offset < nxt < handed_us else handed_us

    # END OF LIST — only on PROOF: the key exists and says has_more=false.
    # A missing/falsy `.get()` is not evidence; reading it as one is how
    # a single page of 500 got to call itself a full scan.
    if pagination.get("has_more") is False:
        stop_reason = "end_of_list"
        walked_whole_list = bool(totals) and totals[-1] > 0 and offset >= totals[-1]  # P2 + A2
        break

    if not batch:
        # The server claims more (or said nothing) yet handed back an
        # empty page: we cannot make progress and we cannot conclude.
        stop_reason = "empty_page"
        break

    tail = parse_ts(batch[-1].get("updated_at") or "")
    if tail is not None and tail < day_start:
        stop_reason = "covered_target_day"   # P1
        tail_passed_day = True
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

# Complete == P1 or P2 held, and the filter worked. See COVERAGE
# CONTRACT above for what those are; this block only evaluates them and
# says, in words the caller can act on, which one failed.
total_last = totals[-1] if totals else None
covered = tail_passed_day or walked_whole_list
incomplete_because = []

if not covered:
    if stop_reason == "page_limit":
        incomplete_because.append(
            "page budget (%d pages) ran out before the scan reached the target "
            "day — raise SUMMARIZER_MAX_PAGES and re-run" % MAX_PAGES)
    elif stop_reason == "empty_page":
        incomplete_because.append(
            "server returned an empty page without saying the list had ended")
    elif total_last is None:
        incomplete_because.append(
            "list claimed to end but reported no total — nothing to check the "
            "scan against")
    elif total_last == 0:
        incomplete_because.append(
            "the whole list came back empty (total=0) — a lost read view looks "
            "exactly like this, so it is not being reported as a quiet day")
    else:
        incomplete_because.append(
            "list claimed to end after %d of %d rows — %d never arrived"
            % (offset, total_last, total_last - offset))
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
                "covered_by": ("tail_passed_day" if tail_passed_day else
                               "whole_list_walked" if walked_whole_list else None),
                "pages_fetched": pages_fetched, "entries_scanned": len(entries),
                "rows_accounted": offset,
                "unparsed_created_at": unparsed_created_at,
                "next_offset_skips_ignored": skips_ignored,
                "oldest_updated_at": oldest_updated_at,
                # total as the server reported it on the first and last
                # page: `list_total_last` is what P2 is checked against,
                # and a difference between the two means the list moved
                # under the scan (entries updated while we paged).
                "list_total_first": totals[0] if totals else None,
                "list_total_last": total_last,
                "incomplete_because": incomplete_because}}
print(json.dumps(out, ensure_ascii=False))

if not complete:
    sys.stderr.write(
        "INCOMPLETE SCAN for %s (stopped: %s after %d pages / %d entries, "
        "oldest updated_at %s):\n%s\n"
        "The counts above are a floor, not the day: do NOT write a journal "
        "from this.\n"
        % (target, stop_reason, pages_fetched, len(entries), oldest_updated_at,
           "".join("  - %s\n" % r for r in incomplete_because).rstrip("\n")))
    sys.exit(3)
PY
