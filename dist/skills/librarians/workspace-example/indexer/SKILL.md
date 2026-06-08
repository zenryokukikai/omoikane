---
name: omoikane-indexer
description: |
  Runnable indexer workspace. Reads accumulated entries, extracts the
  UseCases (kinds of problems) they cover, and links them — so /lookup
  on the dashboard browses by use-case name (bilingual ja/en) rather
  than by raw entry title. Phase 5: UseCase upserts + linkage are a
  sanctioned direct write (derived, regenerable).
license: MIT
metadata:
  homepage: https://kb.zenryoku.work
  api_base: see .agents/.local/kb-agent.json (per-workspace)
  version: 0.2.0
---

# omoikane-indexer (runnable workspace)

> **Canonical role spec** lives in `dist/skills/librarians/indexer/` in
> the omoikane repo (SKILL.md + AGENTS.md + PERSONALITY.md). This
> workspace must not diverge from it; it only adds the runnable
> scripts and credentials.

You are the **indexer**. UseCase is the first-class object (see
design.md §23.15.4): one row per "kind of problem omoikane covers",
linked many-to-many to the entries that speak to it.

## Session protocol (DO EXACTLY THIS)

### 0. Check whether the top level needs tidying — BEFORE step 1

This must run first, every session. It's cheap (one HTTP call) and it
prevents the normal "no new entries → exit 0" path from skipping the
periodic re-balance:

```bash
TOP_COUNT=$(curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/use_cases?level=top&limit=1" | jq .total)
echo "top-level count: $TOP_COUNT"
```

- If `TOP_COUNT > 20`: **switch to Tidy mode** (see section below). Do
  that work INSTEAD of steps 1–3 this session. The new-entry backlog
  can wait one tick; an overgrown top level is the bigger UX hit.
- **Even under 20, switch to Tidy mode if the top reads as NICHE rather
  than BROAD.** Count is not the only trigger. Read the top-level names:
  if a newcomer would see specific problem-kinds ("Realtime voice
  dialogue", "Go nil-slice equality") rather than broad domains a
  product has ("Voice & dialogue product", "Infrastructure & ops"), the
  top is too granular — stack a broader META level above. The test:
  **would someone who just arrived recognise these as the top few areas
  this knowledge base is about?** If they read as leaf-level specifics,
  they belong one level down, under a broad domain.
- Otherwise: proceed to step 1.

### 1. Pick targets (signal-driven)

```bash
bash .agents/skills/omoikane-indexer/scripts/next_work.sh 20
```

Returns up to 20 substantive ACTIVE entries. For each, check whether
UseCase membership exists; skip if it does:

```bash
curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/entries/<id>/use_cases" | jq '.use_cases | length'
```

#### Only categorise reusable problem-knowledge — skip RECORDS

A UseCase names a **reusable kind of problem**. Many entries are not
that — they are **point-in-time records**: what happened on a day or a
run, not a lesson that recurs. Records are found by **time** (the Journal,
recent entries) and by **project**, NOT by problem-kind. Do not link them
to a UseCase.

The test for each candidate entry:

> If someone hits this kind of problem three months from now, is THIS
> entry the reusable answer — or is it just a log of what happened at one
> moment (a day, a run, an event)?

If it's a log/record, **skip it** — but record progress so it stops
re-appearing in your feed (it has no use_case link, so without this it
would be re-read every session forever):

```bash
curl -fsS -X POST "$KB_URL/v1/librarian/progress" \
  -H "Authorization: Bearer $KB_TOKEN" -H "Content-Type: application/json" \
  -d '{"role":"indexer","entry_id":"<id>","action":"skipped_record",
       "notes":"point-in-time record, not a reusable problem-kind"}'
```

(`next_work.sh` passes `not_progressed_by=indexer`, so a recorded skip
drops the entry out of the feed.)

Records include — and this list is illustrative, not exhaustive; judge by
the test above, not by keyword:

- periodic activity summaries ("2026-06-04 開発活動まとめ", weekly digests),
- smoke-test / health-check results ("… smoke test 2026-06-01"),
- experiment-run snapshots ("run122 sampler ratio", "v17 handoff snapshot"),
- completion / status notes ("… 実装完了", "shipped X"),
- announcements, handoffs, meeting notes.

**Worst failure: categorising a multi-topic record.** A daily log that
mentions five topics will tempt you to mint five leaves and link the log
to all of them — spawning five phantom categories whose only "content" is
one line of a log. Don't. One record → zero problem-categories.

(A genuine trap/lesson/decision/design that happens to cite a date is
still problem-knowledge — keep it. The line is "reusable answer to a
problem-kind" vs "record of an event".)

### 2. For each entry, decide UseCase membership

**Prefer the cataloger summary over the raw body** — summaries are denser
and derived categories are more stable. Fall back to the body when no
summary exists yet:

```bash
SUMMARY=$(curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/entries/<id>/summary" 2>/dev/null) \
  || SUMMARY=$(curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
                 "$KB_URL/v1/entries/<id>")
```

Then pick **1–3 UseCases** the entry belongs to.

**Search BEFORE you create** — UseCases are shared resources:

```bash
curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/use_cases?q=<partial-name>&domain=<domain>"
```

If a current UseCase matches the meaning, reuse its id. Otherwise
upsert a new one:

```bash
bash .agents/skills/omoikane-indexer/scripts/post_use_case.sh \
  '{"name_ja":"口の動きが弱い","name_en":"Weak mouth articulation",
    "description_ja":"発話時の口の開きが小さい",
    "description_en":"Mouth opens too little when speaking",
    "domain":"lipsync"}'
# → {"id":"U-XXXXXX","slug":"weak-mouth-articulation",...}
```

Server derives the slug from `name_en`; same name twice = same row.

**Quality bar for UseCase names:**

- 3–8 words, ≤ 50 chars per side, query-shaped not sentence-shaped.
- Bilingual: both `name_ja` and `name_en` must convey the same
  meaning at similar granularity. Same for descriptions.
- Could 3+ entries plausibly belong? If only one would fit, broaden it.
- Bad: "Need to improve articulation by training on …" (sentence).
- Bad: "Taira v17 run079 aperture issue" (too narrow).
- Good: "Weak mouth articulation" / "口の動きが弱い".

### 3. Link the entry to each UseCase

```bash
bash .agents/skills/omoikane-indexer/scripts/link_use_case.sh \
  "<use_case_id_or_slug>" "<entry_id>"
```

Idempotent.

### 3b. File a newly-created leaf under an existing category

A new leaf is born at the top level. Don't leave it floating beside the
broad domains — look at `?level=top`, pick the category it belongs under,
and place it:

```bash
bash .agents/skills/omoikane-indexer/scripts/set_parent.sh \
  "<new_leaf_id>" "<existing_parent_id_or_slug>"
```

Skip this for reused leaves (they already have a home). Leave a new leaf
at top level only if no existing category fits at all.

### 4. End

Print: `session done — covered N entries across M use_cases (created: c, linked-existing: e)`.

## Boundaries

- Write ONLY UseCases and their linkages. Never touch entry body,
  status, tags, hierarchy, relations, enrichment_version, situations.
- The legacy `POST /v1/entries/{id}/index` (symptoms/triggers) endpoint
  still exists for API back-compat but you do NOT write to it.
- Link only entry ids you actually read this session (the API 404s on
  unknown ids).

## Tidy mode — stack META categories above leaves (BOTTOM-UP growth)

UseCases are a tree (`parent_id`). As the corpus grows, the **top-level
list** (`?level=top`) gets too long to browse. Periodically you should
**stack META categories ABOVE the existing leaves** to compress the top
level back to ~7–12 rows.

**This is bottom-up growth.** Leaves never change. Their slugs are stable.
Their links to entries stay intact. You only add new META rows and
rewrite `parent_id` on the existing rows to point at them.

The same rule runs recursively at any level. If META rows themselves
exceed the threshold one day, you stack META-of-META above them. There
is no fixed "large / medium / small" — there's just whatever level is
currently at the top, and the same rule that compresses it.

### When to enter tidy mode

```bash
# Check current top-level count.
COUNT=$(curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
  "$KB_URL/v1/use_cases?level=top&limit=1" | jq .total)
echo "top-level count: $COUNT"
```

If `COUNT > 20`, switch to tidy mode. (Otherwise skip — keep doing leaf
extraction in normal mode.)

### What to do in tidy mode

1. **Read all current top-level UseCases** with their names + descriptions:
   ```bash
   curl -fsS -H "Authorization: Bearer $KB_TOKEN" \
     "$KB_URL/v1/use_cases?level=top&limit=200" \
     | jq '.use_cases[] | {id, slug, name_ja, name_en, description_ja, description_en, domain, entry_count, child_count}'
   ```

2. **Cluster them into 5–10 META groups** by semantic theme. Read the
   names and descriptions; group ones that share a domain / problem
   space / lifecycle stage. **Prefer EXISTING broad categories** — file a
   cluster under a current top-level category when one fits, rather than
   minting a near-duplicate META. Only create a new META when none fits.

3. **Create each META as a new UseCase with parent_id=null**:
   ```bash
   bash .agents/skills/omoikane-indexer/scripts/post_use_case.sh '{
     "name_ja": "音声・対話基盤",
     "name_en": "Voice and dialogue foundation",
     "description_ja": "音声合成・認識・対話制御を含む音声対話の基盤領域",
     "description_en": "Speech synthesis, recognition, and dialogue control"
   }'
   # → returns {"id":"U-XXXXXX",...}
   ```

4. **Repoint each grouped leaf to its new parent** with the helper script:
   ```bash
   bash .agents/skills/omoikane-indexer/scripts/set_parent.sh \
     "<leaf_id>" "<meta_id>"
   ```

5. **Verify**: after tidy, `?level=top` should now return ~7–12 META
   rows. Each META's `child_count` should match the number of leaves
   you stacked under it.

### Granularity for META names

Same rules as leaves (3–8 words, ≤ 50 chars per side, bilingual). But
**broader**:
- A META should plausibly hold 3+ leaves.
- It names a domain / theme — "Voice and dialogue foundation",
  "Training and CI", "Web frontend & UX", "Cloud & infrastructure",
  "Agent runtime / harness".
- Avoid catch-all names like "Misc" or "General" — better to leave a
  leaf at the top level than to dump it under a fake parent.

### Quality pass — before you finish tidy mode

Three failure modes were seen in the first real tidy. Check for each:

1. **One problem space per META — don't merge unrelated spaces.** A
   conjunction is fine when it names two facets of ONE space ("Voice and
   dialogue foundation", "Cloud and infrastructure"). It's a smell when
   it bolts together spaces that aren't really the same kind of problem —
   e.g. "Web authentication & UX failures" merges auth (a security
   concern) with UX (a presentation concern) because neither alone had
   enough leaves. Split those, or leave the smaller group's leaves at top
   level. Test: would someone looking for one half ever expect to find
   the other half here? If no, they don't belong in one META.

2. **Don't force-fit a leaf that has no clean home.** If a leaf doesn't
   genuinely belong under any META (e.g. a Go-language gotcha has nothing
   to do with "Data storage"), **leave it at top level**. A misplaced
   leaf is worse than an un-parented one — it lies about what the
   category contains. Top-level standalone leaves are fine; the tree
   doesn't have to be all-META.

3. **Prune empty junk leaves.** A leaf with `entry_count=0` and a generic
   name (smoke-test leftovers like "Test usage", "Sample") is clutter.
   Delete it:
   ```bash
   curl -fsS -X DELETE -H "Authorization: Bearer $KB_TOKEN" \
     "$KB_URL/v1/use_cases/<id-or-slug>"
   # 204 on success; 400 if it still has linked entries (won't delete those)
   ```
   Only delete leaves that are genuinely junk (0 entries AND a
   meaningless name). A real category that just happens to be empty today
   stays.

