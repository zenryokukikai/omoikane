# Running a librarian (Phase 5)

How to bring a librarian role from a skill bundle (`<role>/`) to a
scheduled process that drains its backlog against a live omoikane
server. This is generic: it names no host, token, path, or schedule —
substitute your own. Mechanism choices (scheduler, runtime) are noted
as examples, not requirements.

## Model

- A **bundle** (`dist/skills/librarians/<role>/`) is the authoritative
  role definition (essence, domains, persona, prohibitions). It is
  per-tick: one backlog entry → one action.
- A **runnable workspace** (outside this repo) wires the bundle to the
  omoikane API: helper scripts + credentials + a batch loop. It is a
  *derivative* of the bundle — it must not diverge from the bundle's
  philosophy, only add the harness.
- A **session** is one runtime invocation that batches many ticks
  (e.g. up to N entries) then exits.
- A **scheduler** fires sessions on a cadence. Any scheduler works
  (launchd, cron, systemd timer, CI loop).

Phase 5 is observation mode: every action is a `librarian_meta`
**DRAFT** proposal. Nothing destructive (status change, supersede,
edge create, delete) executes automatically.

## One-time setup for a new librarian

### 1. Mint a role-scoped invite (admin)

```bash
curl -sS -X POST "<base-url>/v1/admin/agent-invites" \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"librarian_role":"<role>"}'
# → { "code": "<invite-code>", ... }
```

`<role>` is one of: `coordinator | cataloger | curator | detective |
conservator | scout | indexer | summarizer | judge | synthesizer`. The invite binds the
redeemed token to that role; the server rejects cross-role use.

### 2. Redeem it for a dedicated token

```bash
curl -sS -X POST "<base-url>/v1/agents/register" \
  -H "Content-Type: application/json" \
  -d '{"name":"<runtime>-<role>","description":"<what it does>","invitation_code":"<invite-code>"}'
# → { "agent_id": "<user-id>", "api_key": "<token>", ... }
```

Give each librarian its OWN identity (don't reuse a human/agent token)
so the audit trail shows who proposed what, and so the
proposal-acceptance rate is measurable per role.

### 3. Register an instance

```bash
curl -sS -X POST "<base-url>/v1/librarian/instances" \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"role":"<role>","agent_runtime":"<runtime>"}'
# → { "instance_id": "<role>-xxxxxxxx" }
```

### 4. Build the runnable workspace

A workspace directory (outside this repo) contains:

```
<workspace>/
├── AGENTS.md                         # points back to the canonical bundle
└── .agents/
    ├── .local/kb-agent.json          # { kb_core_url, api_key, instance_id, librarian_role }
    └── skills/<runtime>-<role>/
        ├── SKILL.md                  # runnable protocol; "derived_from" the bundle
        └── scripts/                  # load_env, backlog_next, post_*, ...
```

- `kb-agent.json` is the ONLY place the token lives. Never echo,
  commit, or copy it out. Keep a second file (e.g. `kb-agent.local.json`)
  pointed at a local server for validation; switching local↔prod is a
  file swap, not a skill change.
- The SKILL's frontmatter should carry `derived_from:
  dist/skills/librarians/<role>/` and a one-line "do not diverge from
  the canonical bundle" note.
- Helper scripts are generic across roles except the role's own
  `post_*` (what it writes). Reuse `load_env.sh` / `backlog_next.sh` /
  `post_no_action.sh`; add the role-specific producer.

### 5. Validate locally before any production run

Point `kb-agent.json` at a local server seeded with a DB snapshot, and
run a small capped session:

```bash
cd <workspace>
pi --print --skill .agents/skills/<runtime>-<role> --no-context-files \
   "<smoke prompt: process at most 2-3 backlog entries this session>"
```

Then inspect what it wrote (DRAFTs are excluded from search, so read
the progress log and fetch the output entries):

```bash
curl -sS -H "Authorization: Bearer <token>" \
  "<base-url>/v1/librarian/progress?role=<role>&instance_id=<instance-id>"
# follow each output_entry_id with GET /v1/entries/<id>
```

Confirm: outputs are `status=DRAFT`; the role stayed inside its
owned domain; nothing destructive happened. Delete test entries when
done.

### 6. Schedule sessions

Wrap the session in a small script that sets a minimal PATH (schedulers
give a bare environment), takes a lock so sessions don't overlap, and
invokes the runtime. Then register it with your scheduler.

- Pick a cadence that catches newly-arrived entries; since each session
  already drains a batch, low frequency is fine.
- **Stagger** multiple librarians so their sessions don't all fire at
  once (e.g. offset their intervals).
- The runtime reads its model credentials from its own config (not from
  the scheduler env); the workspace `kb-agent.json` supplies the
  omoikane credentials. Verify the runtime works under a minimal
  environment before trusting the schedule.

### 7. Record where it runs

Keep an operator note (outside this repo — it contains host/path/ID
specifics) with: the instance id, workspace path, scheduler entry,
log locations, and the start/stop commands. This repo holds the
generic procedure; the deployment specifics are environment-private.

## Emergency stop

Set an instance's status to `stopped` (operator/admin action); each
tick then heartbeats but does not act — the `backlog_next` helper
honours it automatically. To fully halt, remove the scheduler entry.

## Reference implementations — copy-paste examples

Complete, runnable, secret-free workspace skeletons for six roles live
in **[`workspace-example/`](workspace-example/)** (cataloger, detective,
curator, scout, indexer, summarizer) — scripts + session wrapper + LaunchAgent,
with placeholders for host/path/token. Start there: copy the closest
role, fill the placeholders, validate locally, schedule. See
[`workspace-example/README.md`](workspace-example/README.md) for the
step-by-step.

When adding a NEW role, copy the closest existing example and swap the
bundle + the role-specific producer script (`post_*` / `fetch_*`).
