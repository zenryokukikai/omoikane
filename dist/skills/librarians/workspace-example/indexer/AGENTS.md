# omoikane-indexer workspace — agent notes

> **Canonical role spec:** `dist/skills/librarians/indexer/` in the
> omoikane repo (AGENTS.md + SKILL.md + PERSONALITY.md). This workspace
> must not diverge — it only adds runnable scripts + credentials.

## What this workspace is

A runnable home for the indexer librarian: it reads accumulated entries
and writes the reverse-lookup index (`symptoms_index` / `triggers_index`)
via `POST /v1/entries/{id}/index`, reviving `/v1/lookup/by-symptom|trigger`
and `/v1/index`. Without it those stay empty (omoikane has no LLM).

## Layout

- `.agents/skills/omoikane-indexer/` — the skill (SKILL.md + scripts).
- `.agents/.local/kb-agent.json` — per-workspace credentials
  (`kb_core_url`, `api_key`, `instance_id`, `librarian_role:"indexer"`).
  NEVER commit or echo this file.
- `scripts/` — `load_env.sh`, `next_work.sh`, `post_index.sh`.
- `run-session.sh` — launchd/cron wrapper (one pi session per fire).

## Boundaries (mirror of the canonical spec)

- Write only the reverse index. No edits to entry content, status,
  tags, hierarchy, relations, enrichment_version, situations.
- Phrases must be grounded in entry content and bilingual (JA + EN).
- Index writes are a sanctioned direct write (derived, regenerable),
  not DRAFT proposals — `source` records authorship.
- Signal-driven targets: index missing/stale entries, not the whole KB.
