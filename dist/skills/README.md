# omoikane skill distribution

Ready-to-install skill packages for various agent runtimes. Each package
gives the agent access to the omoikane Agent Knowledge Base via the
`kb-mcp` stdio adapter.

| Package | Target | Install location |
|---|---|---|
| [`claude-code/`](claude-code/) | Claude Code | `~/.claude/skills/omoikane-kb/` |
| [`opencode/`](opencode/) | OpenCode | `~/.opencode/skills/omoikane-kb/` |
| [`generic-stdio-mcp/`](generic-stdio-mcp/) | Cursor / Cline / any MCP-stdio runtime | (runtime-specific) |
| [`librarians/`](librarians/) | Phase 5+ Librarian Community (8 roles) | (reserved) |

## Prerequisites

1. `kb-server` running (default `:8080`).
2. An API token issued via `kb-server admin-token --user me --scopes read,write`.
3. `kb-mcp` on the agent's `PATH`.

See the README in each package directory for runtime-specific details.
