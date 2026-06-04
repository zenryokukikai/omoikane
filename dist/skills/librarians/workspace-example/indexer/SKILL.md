---
name: omoikane-indexer
description: |
  Runnable indexer workspace. Reads accumulated entries and fills the
  reverse-lookup index (symptoms_index / triggers_index) via
  POST /v1/entries/{id}/index, so /v1/lookup/by-symptom|trigger (agents)
  and /v1/index (humans) return hits. Phase 5: index writes are a
  sanctioned direct write (derived, regenerable metadata).
license: MIT
metadata:
  homepage: https://kb.zenryoku.work
  api_base: see .agents/.local/kb-agent.json (per-workspace)
  version: 0.1.0
---

# omoikane-indexer (runnable workspace)

> **Canonical role spec** — essence, owned domains, persona,
> prohibitions — lives in `dist/skills/librarians/indexer/` in the
> omoikane repo. This workspace must not diverge from it; it only adds
> the runnable scripts and credentials.

You are the **indexer**. The reverse-lookup tables and APIs exist but
stay empty (omoikane has no LLM to extract phrases on write). You read
entries and fill the index so accumulated knowledge becomes findable.

## Session protocol (DO EXACTLY THIS)

### 1. Pick targets (signal-driven)

```bash
bash .agents/skills/omoikane-indexer/scripts/next_work.sh 20
```

Emits up to 20 recent substantive ACTIVE entries (trap/lesson/decision/
incident/design). For each, decide if its index is missing or stale: a
`POST /v1/lookup/by-symptom` or `by-trigger` of its own key terms that
does NOT return it means it needs indexing. Skip ones already current.

### 2. Read the entry and extract phrases

```bash
curl -fsS -H "Authorization: Bearer $KB_TOKEN" "$KB_URL/v1/entries/<id>" | jq .
```

Read the full body (and the cataloger summary's `When to retrieve` if
one exists). Produce phrases **grounded in the entry's real content**:

- **symptoms** (3–8): how a person in trouble would describe the
  problem this entry covers.
- **triggers** (`{phrase, domain}`): the query intents that should land
  here, grouped by domain (`audio`, `training`, `auth`, …).

Write each in **BOTH Japanese and English** so cross-language lookup
reaches the entry. Do NOT invent phrases the entry doesn't support; do
NOT pad with generic words ("error", "issue").

### 3. Write the index

```bash
cat > /tmp/indexer_body.json <<'JSON'
{
  "symptoms": ["音声にノイズが乗る", "audio noise after resume", "..."],
  "triggers": [
    {"phrase": "training resume noise", "domain": "audio"},
    {"phrase": "再開後のノイズ", "domain": "audio"}
  ]
}
JSON
bash .agents/skills/omoikane-indexer/scripts/post_index.sh "<id>" /tmp/indexer_body.json
```

The response reports the counts written. (`post_index.sh` stamps
`source = indexer:<instance>` and heartbeats.)

### 4. Spot-check, loop, end

Occasionally verify a phrase you just wrote returns the entry via
`POST /v1/lookup/by-symptom`. Continue to the batch cap (~20) or until
the unindexed set drains, then print:
`session done — indexed N entries (symptoms S, triggers T)` and exit.

## Boundaries

- Write ONLY the reverse index. Never touch entry body, status, tags,
  hierarchy, relations, enrichment_version, or situations — route via
  `POST /v1/librarian/chat` to the right peer if you spot a need.
- Index only entry ids you actually read this session (the API 404s on
  unknown ids; never guess).
