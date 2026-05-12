# omoikane Librarian Community skills (Phase 5)

The 8-role librarian community per `docs/design.md` §23. Each role is a
self-contained skill bundle that any compatible agent runtime
(Claude Code / OpenCode / a future custom runtime) loads to *play* the
role. The Core does NOT execute these — see §23.6 "Skill 抽象化".

## Roles

| Role | Owns | Triggers |
|---|---|---|
| `coordinator/` | tasks queue / budget / escalation | anomalies, budget pressure, repeated specialist failures |
| `cataloger/` | tags / hierarchy / situations | new entry, tag threshold, hierarchy imbalance |
| `curator/` | status / relations (conflict) | signal drift, conflict detection |
| `detective/` | incidents / clusters / relations (discovery) | incident accumulation, periodic scans |
| `conservator/` | enrichment_version / dead_pool / schema | version drift, dormancy threshold |
| `scout/` | external_findings | heartbeat, interest-domain freshness |
| `summarizer/` | chat_threads closing | thread end-condition fires |
| `judge/` | quartet_assignments | quartet discussion concludes |

## Per-role layout

Each `<role>/` directory contains the 10 files mandated by §23.6:

```
<role>/
├── SKILL.md                # frontmatter + load order + prohibitions
├── role_definition.md       # essence, owned domains, success criteria
├── personality.yaml         # personality DSL (§23.3)
├── operations.yaml          # API whitelist
├── decision_protocols.md    # if-then judgement procedures
├── trigger_conditions.yaml  # heartbeat / reactive / idle triggers
├── communication_style.md   # tone + few-shot examples
├── meta_protocol.md         # meta-entry recording format
├── error_handling.md        # failure-mode responses
├── self_check.md            # pre-action checklist
├── examples/                # good / bad judgement examples
└── sub_agents/              # RESERVED: Phase 6 fractal hierarchy (§24).
                              #   Empty in Phase 5; do not delete.
```

## Phase 5 status

**Observation mode only.** All actions produce **draft proposals**
recorded as `librarian_meta` entries — no destructive writes (status
changes, supersede edges, tag merges) execute automatically. Promotion
to active mode is Phase 6.

## librarian-runner

The runtime harness that loads a skill bundle and delegates execution
to a configured agent runtime is `cmd/librarian-runner`. Phase 5 ships a
**stub** that:
- Validates skill files exist
- Registers the instance with the Core
- Records a heartbeat at the configured cadence
- Posts a placeholder chat message announcing "OBSERVING"

The real LLM-call loop is delegated to the agent runtime in Phase 6+.
