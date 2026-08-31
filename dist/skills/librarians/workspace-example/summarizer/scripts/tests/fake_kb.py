#!/usr/bin/env python3
"""A deliberately misbehaving stand-in for GET /v1/entries.

Each MODE reproduces one way a paging client can believe it saw a whole
day when it did not. fetch_yesterday.sh must either get the day right or
say `scan.complete=false` — never return a confident zero.

Usage: fake_kb.py <port> <mode>

Modes:
  honest        well-behaved server (full pages, truthful pagination)
  clamp         ignores ?limit and returns 137 per page, next_offset honest
                (a client that advances by its OWN page size steps over
                the remainder — the /v1/use_cases limit>200 -> 30 bug)
  nopagination  omits the "pagination" object entirely
  empty_after_first  page 2+ is empty while has_more still says true
  short_list    page 1 says has_more=false although the list goes on
  unparsable    like honest, but 3 target-day created_at values are junk
  skipping_next_offset  next_offset jumps far past the rows it just sent
                (following it silently leaves a hole in the middle)
  lost_view     has_more=false, entries=[], but total still says 18k —
                the shape of an ACL/space regression: the view is gone,
                not the day
  empty_kb      has_more=false, entries=[], total=0

The data set: 2026-08-10..2026-08-31 JST, 200 entries per day. The mix
DIFFERS PER DAY (external_finding = 5 + day%5, lesson = 10 + day%3, one
daily_journal the script must drop, the rest cataloger_summary), so a
scan that is off by one day cannot pass. The first entry of a day sits
at 00:00:00.000000000 JST and the last at 23:59:59.999999999 JST, so any
slip in the JST boundary moves the counts too. Fraction widths cycle
through 9/8/6/5/3/0 digits the way Go's RFC3339Nano emits them (it drops
trailing zeros).

`expected.py <YYYY-MM-DD>` prints the fixture's own answer for a day, so
the tests never hard-code a number this file can drift away from.
"""
import datetime
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse

JST = datetime.timezone(datetime.timedelta(hours=9))
UTC = datetime.timezone.utc
FRACTIONS = [9, 8, 6, 5, 3, 0]
PER_DAY = 200


def stamp(dt, digits):
    """RFC3339 in UTC with `digits` fractional digits (0 = none)."""
    u = dt.astimezone(UTC)
    base = u.strftime("%Y-%m-%dT%H:%M:%S")
    if digits == 0:
        return base + "Z"
    nanos = "%09d" % (u.microsecond * 1000)
    return base + "." + nanos[:digits] + "Z"


def mix(day):
    """(external_findings, lessons) for a JST day — deliberately uneven."""
    return 5 + day % 5, 10 + day % 3


def build():
    entries = []
    n = 0
    for day in range(10, 32):
        start = datetime.datetime(2026, 8, day, 0, 0, 0, tzinfo=JST)
        n_ext, n_lesson = mix(day)
        for i in range(PER_DAY):
            if i == 0:
                dt = start
            elif i == PER_DAY - 1:
                dt = start + datetime.timedelta(days=1) - datetime.timedelta(microseconds=1)
            else:
                dt = start + datetime.timedelta(seconds=i * 400)
            digits = FRACTIONS[n % len(FRACTIONS)]
            created = stamp(dt, digits)
            if i < n_ext:
                etype, meta = "external_finding", {"source_url": "https://example.test/%d" % n}
            elif i < n_ext + n_lesson:
                etype, meta = "lesson", {}
            elif i == n_ext + n_lesson:
                etype, meta = "librarian_meta", {"kind": "daily_journal"}
            else:
                etype, meta = "librarian_meta", {"kind": "cataloger_summary"}
            entries.append({
                "id": "T-%05d" % n, "type": etype, "title": "e%d" % n,
                "project_id": "omoikane", "body": "body %d" % n,
                "created_at": created, "updated_at": created, "metadata": meta,
            })
            n += 1
    # The list is ordered updated_at DESC.
    entries.sort(key=lambda e: e["updated_at"], reverse=True)
    return entries


ALL = build()
MODE = "honest"


def page_for(offset, limit):
    """(entries, pagination-or-None) for this request under MODE."""
    if MODE == "clamp":
        limit = 137
    batch = ALL[offset:offset + limit]
    total = len(ALL)
    if MODE == "empty_after_first" and offset > 0:
        return [], {"limit": limit, "offset": offset, "total": total,
                    "next_offset": offset, "has_more": True}
    if MODE == "short_list" and offset == 0:
        return batch, {"limit": limit, "offset": offset, "total": total,
                       "next_offset": offset + len(batch), "has_more": False}
    if MODE == "skipping_next_offset":
        # Sends 200 rows, then says "carry on 600 further along". A client
        # that believes it never sees the 400 in between and still calls
        # the scan complete.
        batch = ALL[offset:offset + 200]
        return batch, {"limit": limit, "offset": offset, "total": total,
                       "next_offset": offset + 600,
                       "has_more": offset + 600 < total}
    if MODE == "lost_view":
        return [], {"limit": limit, "offset": offset, "total": total,
                    "next_offset": offset, "has_more": False}
    if MODE == "empty_kb":
        return [], {"limit": limit, "offset": offset, "total": 0,
                    "next_offset": offset, "has_more": False}
    if MODE == "unparsable" and offset == 0:
        # Junk created_at on live entries: they must not be silently dropped.
        batch = [dict(e) for e in batch]
        for e in batch[:3]:
            e["created_at"] = "2026-08-20T99:99:99.xyzZ"
    pagination = {"limit": limit, "offset": offset, "total": total,
                  "next_offset": offset + len(batch),
                  "has_more": offset + len(batch) < total}
    if MODE == "nopagination":
        return batch, None
    return batch, pagination


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        if not parsed.path.startswith("/v1/entries"):
            self.send_error(404)
            return
        q = parse_qs(parsed.query)
        limit = int(q.get("limit", ["50"])[0])
        offset = int(q.get("offset", ["0"])[0])
        batch, pagination = page_for(offset, limit)
        payload = {"entries": batch}
        if pagination is not None:
            payload["pagination"] = pagination
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1])
    MODE = sys.argv[2] if len(sys.argv) > 2 else "honest"
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
