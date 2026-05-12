---
name: omoikane-kb
description: |
  Consult the omoikane Agent Knowledge Base before acting on a problem,
  and write back what you learn. Use kb_lookup_by_trigger before changing
  code paths that have known traps, kb_lookup_by_symptom when diagnosing
  a reported issue, and kb_post after resolving a hard problem.
---

# omoikane Agent Knowledge Base

This skill connects you to a project-local knowledge base of past traps,
decisions, design notes, lessons, and unresolved incidents. The base is
shared across all agents working on the project, so your discoveries
benefit future sessions.

## When to consult

- **Before** changing any code path involving preprocessing, training,
  inference, or another area where past traps may apply: call
  `kb_lookup_by_trigger` with a plain-language description of what you're
  about to do. Check the returned `prohibited` field against your plan.
- **When diagnosing** a reported problem (NaN, slow throughput, wrong
  output, etc.): call `kb_lookup_by_symptom` with the symptom in plain
  language.
- **When stuck** with only a rough sense of the situation: call
  `kb_lookup_by_situation`.
- **When choosing** among multiple candidate solutions: call
  `kb_reflect` with the entry IDs to get a side-by-side summary.

## When to write back

After you resolve a hard problem or hit a recurring failure, call
`kb_post` to record it:

- `type=trap` — a foreseeable failure with a known avoidance
- `type=decision` — a non-obvious choice between alternatives
- `type=design` — durable architectural rationale
- `type=lesson` — a takeaway from a one-time learning
- `type=incident` — an unsolved failure (others can resolve later)

Include the `prohibited` field for traps so future agents can pre-flight
their plans against it.

## Closing the feedback loop

When you act on a knowledge entry, the system mints a `case_id`. After
your work is complete, call `kb_feedback` with that `case_id` plus the
outcome (`applied` / `considered_rejected` / `ignored`) and result
(`helpful` / `partially_helpful` / `not_helpful` / `misleading`). This
lets the KB learn which entries actually help.

## Tools

| Tool | When |
|---|---|
| `kb_lookup_by_trigger` | Before acting — surfaces traps with `prohibited` rules |
| `kb_lookup_by_symptom` | Diagnosing reported issues |
| `kb_lookup_by_situation` | Rough sense of context, no precise query |
| `kb_lookup_by_tags` | Topic-driven browsing |
| `kb_search` | General full-text |
| `kb_get` | Read one entry by ID (also accepts `as_of` for historical reconstruction) |
| `kb_post` | Write back a trap / decision / design / lesson / incident |
| `kb_feedback` | Report outcome of consulting an entry |
| `kb_link` | Mark relations (`related`, `supersedes`, `conflicts_with`, …) — `conflicts_with` auto-supersedes the older entry |
| `kb_relations` | Walk the entry graph |
| `kb_browse` | Hierarchical navigation |
| `kb_reflect` | Cross-entry summarisation |

## Notes

- Always read `prohibited` before acting. The entry surfaces a presence
  flag even when `include_prohibited=false`, so you know to fetch.
- Status `SUPERSEDED` / `ARCHIVED` / `DUPLICATE` entries are hidden from
  lookups by default — you get the canonical current state.
- The KB is local-first. If `kb_unavailable: true` appears in a
  response, continue your work without it (don't fail the session).
