# MCP Support

fir integrates with external [Model Context Protocol](https://modelcontextprotocol.io) (MCP)
servers. Each MCP server runs as a local stdio subprocess and exposes a set of tools that the
agent can call alongside its built-in tools (read, bash, edit, etc.). This lets you extend fir
with any MCP-compatible tool server without modifying fir itself.

---

## Configuration file — `.fir/mcp.json`

Place a `mcp.json` file in the `.fir/` directory at the root of your project. fir reads it
automatically when starting a session.

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"],
      "env": {
        "NODE_PATH": "/usr/local/lib/node_modules"
      }
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"]
    }
  }
}
```

### Fields

| Field     | Type              | Required | Description |
|-----------|-------------------|----------|-------------|
| `command` | string            | yes      | Executable to launch (looked up on `PATH`) |
| `args`    | array of strings  | no       | Command-line arguments passed to the server |
| `env`     | map string→string | no       | Environment variable overrides for the subprocess; the parent process environment is always inherited first |

Each top-level key under `mcpServers` is the **server name** — choose something short and
descriptive, as it appears in every tool name the server exposes.

---

## How tools appear

When fir connects to an MCP server it calls `tools/list` and registers each tool with the
agent. Tool names are prefixed to avoid collisions with built-in tools:

```
mcp__<serverName>__<toolName>
```

For example, if the server named `filesystem` exposes a tool called `read_file`, it appears to
the agent as:

```
mcp__filesystem__read_file
```

The tool's description and parameter schema are passed through unchanged from the MCP server, so
the LLM sees the same documentation the tool server provides.

---

## ACP injection — passing `mcpServers` in `session/new`

When fir is running in ACP mode (e.g., inside a VS Code extension), an ACP client can supply
MCP server configurations directly in the `session/new` request without requiring a
`.fir/mcp.json` file on disk.

Extend the standard `session/new` JSON payload with a top-level `mcpServers` field that follows
the same schema as the config file:

```json
{
  "cwd": "/home/user/myproject",
  "mcpServers": {
    "database": {
      "command": "/usr/local/bin/my-db-mcp-server",
      "args": ["--dsn", "postgres://localhost/mydb"]
    }
  }
}
```

**Precedence:** if a server name appears in both the project-level `.fir/mcp.json` *and* the
`session/new` request, the **request-level entry wins**. This lets a client override or
supplement project defaults without modifying files on disk.

---

## Limitations

- **stdio transport only.** fir currently launches every MCP server as a local subprocess and
  communicates over stdin/stdout. Remote servers using SSE (HTTP transport) are not yet
  supported.
- **No hot-reload.** MCP servers are started once at session creation. Adding or removing
  servers requires starting a new session.
- **No per-server authentication UI.** If an MCP server requires credentials, pass them via
  `env` in the config or ensure they are already in the environment. fir does not prompt for
  MCP credentials interactively.
- **Error isolation is per-server.** If one MCP server fails to start, the entire `session/new`
  request is rejected. Servers that exit mid-session cause subsequent tool calls to that server
  to return errors; other servers and built-in tools remain unaffected.
