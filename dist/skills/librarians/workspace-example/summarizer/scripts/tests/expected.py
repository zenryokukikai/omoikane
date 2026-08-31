#!/usr/bin/env python3
"""Print what fetch_yesterday.sh must report for one day of the fixture.

Derived from the data fake_kb.py actually serves, so the tests never
hard-code a number the fixture can drift away from.

Usage: expected.py YYYY-MM-DD   ->  {"external_findings": .., "first_ext_id": ..}

Covers the UseCase tree (tree_snapshot) as well as the day's entries.
"""
import datetime
import json
import re
import sys

import fake_kb

KNOWLEDGE_TYPES = {"trap", "lesson", "decision", "incident", "design"}


def jst_date(iso):
    s = iso.replace("Z", "+00:00")
    s = re.sub(r"\.(\d+)", lambda m: "." + (m.group(1) + "000000")[:6], s)
    return datetime.datetime.fromisoformat(s).astimezone(fake_kb.JST).strftime("%Y-%m-%d")


target = sys.argv[1]
day = sorted((e for e in fake_kb.ALL if jst_date(e["created_at"]) == target),
             key=lambda e: e["id"])
ext = [e for e in day if e["type"] == "external_finding"]
know = [e for e in day if e["type"] in KNOWLEDGE_TYPES]
meta = [e for e in day if e["type"] == "librarian_meta"
        and (e.get("metadata") or {}).get("kind") != "daily_journal"]

# The UseCase tree, by the same rule: derived from what fake_kb serves.
ucs = fake_kb.USE_CASES
uc_created = [u for u in ucs if jst_date(u["created_at"]) == target]
uc_touched = [u for u in ucs if jst_date(u["created_at"]) != target
              and jst_date(u["updated_at"]) == target]
# A leaf is a UseCase with no children; child_count > 0 makes it a meta.
uc_empty_leaves = [u for u in ucs if not u["child_count"] and not u["entry_count"]]

print(json.dumps({
    "external_findings": len(ext),
    "new_knowledge": len(know),
    "librarian_meta": len(meta),
    "day_rows": len(day),
    "first_id": day[0]["id"],      # 00:00:00.000000000 JST
    "last_id": day[-1]["id"],      # 23:59:59.999999999 JST
    "first_ext_id": ext[0]["id"],
    "total_use_cases": len(ucs),
    "top_level_count": fake_kb.UC_TOP,
    "use_cases_created": len(uc_created),
    "use_cases_touched": len(uc_touched),
    "empty_leaves": len(uc_empty_leaves),
}))
