# omoikane generic stdio MCP package

For any agent runtime that speaks the standard MCP stdio protocol but
doesn't have a dedicated skill convention (Cursor, Cline, custom
runtimes, etc.).

## Files

- `mcp.json` — generic MCP server entry. Most runtimes accept this shape.
- `tools.md` — documentation of the tool surface for prompt authors.
- `system-prompt.md` — drop-in instructions you can paste into your
  agent's system prompt to teach it when to use each tool.

## Setup

1. Start `kb-server`.
2. Issue a token (`kb-server admin-token --user me --scopes read,write`).
3. Point your agent at `kb-mcp` with `KB_CORE_URL` + `KB_TOKEN` in the
   environment. The shape is:

```bash
KB_CORE_URL=http://localhost:8080 \
KB_TOKEN=... \
kb-mcp
```

The agent communicates over stdio using newline-delimited JSON-RPC 2.0
per the standard MCP spec (`initialize` / `tools/list` / `tools/call`).
