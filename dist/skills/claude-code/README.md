# omoikane skill for Claude Code

Drop this directory at `~/.claude/skills/omoikane-kb/` (or a project-local
`.claude/skills/omoikane-kb/`) to give Claude Code access to the omoikane
Agent Knowledge Base.

## Files

- `SKILL.md` — the skill definition Claude Code reads at session start.
- `mcp.json` — MCP server config snippet (merge into your `mcp_settings.json`).

## Setup

1. Start `kb-server` (default `:8080`).
2. Issue an API token: `kb-server admin-token --user me --scopes read,write`.
3. Copy this directory to `~/.claude/skills/omoikane-kb/`.
4. Add to your Claude Code MCP config (`~/.claude/mcp_settings.json`):

```json
{
  "mcpServers": {
    "omoikane": {
      "command": "/usr/local/bin/kb-mcp",
      "env": {
        "KB_CORE_URL": "http://localhost:8080",
        "KB_TOKEN": "<your token>"
      }
    }
  }
}
```

The skill instructs Claude to:
- Run `kb_lookup_by_trigger` before making changes, to surface known traps.
- Run `kb_lookup_by_symptom` before diagnosing reported problems.
- After resolving a hard problem, write a trap/decision/lesson via `kb_post`.
- Record the outcome of consulted entries via `kb_feedback`.
