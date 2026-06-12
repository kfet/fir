# MCP Support

fir integrates with external [Model Context Protocol](https://modelcontextprotocol.io) (MCP)
servers. Each MCP server runs as a local stdio subprocess and exposes a set of tools that the
agent can call alongside its built-in tools (read, bash, edit, etc.). This lets you extend fir
with any MCP-compatible tool server without modifying fir itself.

---

## Configuration file — `.fir/mcp.json`

Place a `mcp.json` file in the `.fir/` directory at the root of your project. fir reads it
automatically when starting a session.

### Stdio servers (local subprocess)

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

### HTTP servers (remote)

Use `"transport": "streamable"` (recommended) or `"transport": "sse"` to connect to
a remote MCP server over HTTP. No `command` is needed — only `url`.

```json
{
  "mcpServers": {
    "my-remote": {
      "transport": "streamable",
      "url": "https://my-server.example.com/mcp"
    },
    "legacy-sse": {
      "transport": "sse",
      "url": "https://old-server.example.com/sse"
    }
  }
}
```

### Fields

| Field       | Type              | Required | Description |
|-------------|-------------------|----------|-------------|
| `transport` | string            | no       | `"stdio"` (default), `"sse"`, or `"streamable"` |
| `url`       | string            | for `sse`/`streamable` | HTTP endpoint of the remote MCP server |
| `command`   | string            | for `stdio` | Executable to launch (looked up on `PATH`) |
| `args`      | array of strings  | no       | Command-line arguments passed to a stdio server |
| `env`       | map string→string | no       | Environment variable overrides for a stdio subprocess; the parent process environment is always inherited first |
| `roots`     | array of strings  | no       | `file://` URIs advertised to the server as filesystem roots; defaults to the working directory |

Each top-level key under `mcpServers` is the **server name** — choose something short and
descriptive, as it appears in every tool name the server exposes.

---

## Drop-in configs (`mcp.d/`)

In addition to `mcp.json`, you can place individual config files in the global `mcp.d/`
directory (`~/.config/fir/mcp.d/`). Each file must be a JSON file (`.json` suffix) with
the same schema as `mcp.json`.

**Resolution order:**
1. `~/.config/fir/mcp.json` (global main config)
2. `~/.config/fir/mcp.d/*.json` (global drop-ins, lexicographically sorted)
3. `.fir/mcp.json` (project-level main config)

Later entries shadow earlier ones when server names collide. fir reports collisions when you
run `/mcp reload` or `/reload`. Note: project shadowing global configs is expected and not
reported as a collision.

**Use cases:**
- Per-tool configs that don't clutter the main file
- Atomic writes (one file per purpose — swap without editing a shared file)
- Machine-generated configs that coexist with hand-edited main config

**Example:** drop a `database.json` into `~/.config/fir/mcp.d/`:

```json
{
  "mcpServers": {
    "pg": {
      "command": "/usr/local/bin/pg-mcp",
      "args": ["--dsn", "postgres://localhost/mydb"]
    }
  }
}
```

**Activating changes:** run `/mcp reload` to re-read configs without restarting the session, or
`/reload` for a full reload. Collisions appear in the output.

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

- **No per-server authentication UI.** If an MCP server requires credentials, pass them via
  `env` in the config or ensure they are already in the environment. fir does not prompt for
  MCP credentials interactively.
- **Error isolation is per-server.** If one MCP server fails to start, the entire `session/new`
  request is rejected. Servers that exit mid-session cause subsequent tool calls to that server
  to return errors; other servers and built-in tools remain unaffected.
