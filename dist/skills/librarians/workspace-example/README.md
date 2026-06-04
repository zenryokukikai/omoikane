# Runnable librarian workspace examples

Copy-paste-ready workspaces for running each librarian as a scheduled
`pi --print` process. The canonical *role definition* lives in
`dist/skills/librarians/<role>/` (essence, persona, prohibitions); the
folders here are the *runnable harness* that wires a role to the
omoikane API — helper scripts, a session wrapper, and a scheduler unit.

These are **examples**: no hosts, tokens, or absolute paths are baked
in. Placeholders (`<WORKSPACE>`, `<NODE_BIN_DIR>`, `<LABEL>`, `<HOME>`)
are yours to fill. The full generic procedure is in
[`../RUNNING.md`](../RUNNING.md); this is the concrete starting point.

## What's here

```
workspace-example/
├── cataloger/   batch summaries        (interval ~30m)
├── detective/   relation proposals     (interval ~45m)
├── curator/     resolve proposals      (interval ~60m)
├── scout/       external findings      (interval ~3h)  — HN/arXiv/HF + SQLite seen-store
├── indexer/     reverse-lookup index   (interval ~90m) — fills symptoms/triggers via /v1/entries/{id}/index
└── summarizer/  daily journal (ACTIVE) (calendar, morning)
```

Each `<role>/` is self-contained:

```
<role>/
├── SKILL.md                 # runnable protocol (derived from the canonical bundle)
├── AGENTS.md                # workspace notes
├── scripts/                 # load_env + role-specific producers
├── run-session.sh.example   # the launchd/cron wrapper
└── launchd.plist.example    # a macOS LaunchAgent (cron/systemd: ignore, schedule the wrapper)
```

## Set up one librarian (≈5 steps)

1. **Copy** a role folder to a workspace dir of your own, into the
   layout the scripts expect:

   ```
   <workspace>/
   ├── AGENTS.md
   └── .agents/
       ├── .local/kb-agent.json          # credentials (you create this; never commit)
       └── skills/omoikane-<role>/
           ├── SKILL.md
           └── scripts/...
   ```

2. **Mint a token + instance** for the role and write `kb-agent.json`
   (see `../RUNNING.md` §1–4). Shape:

   ```json
   { "kb_core_url": "https://<your-omoikane>",
     "api_key": "<role-scoped token>",
     "instance_id": "<role>-xxxxxxxx",
     "librarian_role": "<role>" }
   ```

3. **Validate locally** — point `kb_core_url` at a local server first:

   ```bash
   cd <workspace>
   pi --print --skill .agents/skills/omoikane-<role> --no-context-files \
      "<a small smoke prompt — see the role's SKILL.md>"
   ```

4. **Wrapper**: copy `run-session.sh.example` → `<workspace>/run-session.sh`,
   replace `<NODE_BIN_DIR>` (dir with `pi`) and `<WORKSPACE>`,
   `chmod +x` it.

5. **Schedule**: copy `launchd.plist.example` →
   `~/Library/LaunchAgents/<LABEL>.plist`, replace `<LABEL>`,
   `<WORKSPACE>`, `<HOME>`, then
   `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/<LABEL>.plist`.
   (cron/systemd: skip the plist, just schedule `run-session.sh`.)

## Notes that save you pain

- **Credentials live only in `.agents/.local/kb-agent.json`** — never
  echo, commit, or copy it out. The example scripts contain no secrets.
- **local↔prod is a credential-file swap**, not a code change. Keep a
  `kb-agent.local.json` pointed at a local server for validation.
- **The runtime's model credentials** are separate (e.g. pi reads
  `~/.pi/agent/auth.json`); the wrapper does not pass them via env.
- **Stagger** multiple roles' intervals so their sessions rarely overlap.
- **scout** keeps a SQLite archive of every item it has evaluated
  (`scripts/seen_store.py`: url/title/body/lang/pubdate/action) — both
  the dedup ledger and a reusable asset. It scales to 100k+ via indexed
  lookups.

## Roles without an example yet

`coordinator`, `conservator`, and `judge` have canonical bundles but no
runnable example here yet. They follow the same shape — copy the
closest existing role, swap the bundle and the role-specific producer
script, and follow `../RUNNING.md`.
