# omoikane skill for OpenCode

OpenCode reads MCP-style tool definitions but uses a slightly different
config layout than Claude Code. Drop this directory next to your OpenCode
config (`~/.opencode/skills/omoikane-kb/` or project-local
`.opencode/skills/omoikane-kb/`).

## Files

- `SKILL.md` — the skill definition (identical content to the Claude
  Code variant; OpenCode reads the same markdown).
- `mcp.json` — server config snippet to merge into your OpenCode MCP config.

## Setup

1. Start `kb-server` (default `:8080`).
2. `kb-server admin-token --user me --scopes read,write`.
3. Copy this directory to `~/.opencode/skills/omoikane-kb/`.
4. Merge `mcp.json` into your OpenCode MCP servers config.

The setup process and tool semantics are identical to the Claude Code
variant — see `dist/skills/claude-code/README.md` for full details.
