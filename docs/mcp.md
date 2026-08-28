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
| `url`       | string            | for `sse`/`streamable` | HTTP endpoint of the remote MCP server. Must be `https://` unless the host is loopback — see [Transport security](#transport-security) |
| `command`   | string            | for `stdio` | Executable to launch (looked up on `PATH`) |
| `args`      | array of strings  | no       | Command-line arguments passed to a stdio server |
| `env`       | map string→string | no       | Environment variable overrides for a stdio subprocess; the parent process environment is always inherited first |
| `roots`     | array of strings  | no       | `file://` URIs advertised to the server as filesystem roots; defaults to the working directory |
| `auth`      | object            | no       | Authentication overrides for `sse`/`streamable` servers; see [Authentication](#authentication-oauth). Absent by default — a spec-compliant server needs none |

Each top-level key under `mcpServers` is the **server name** — choose something short and
descriptive, as it appears in every tool name the server exposes.

---

## Authentication (OAuth)

**Zero configuration is the default and the main path.** A bare entry

```json
{"mcpServers": {"my-remote": {"transport": "streamable", "url": "https://my-server.example.com/mcp"}}}
```

already authenticates against any server that implements the MCP authorization
specification. fir connects unauthenticated and, on a `401`, runs the full discovery chain:

1. **RFC 9728 protected-resource metadata** — from the `resource_metadata` parameter of the
   `WWW-Authenticate` challenge, falling back to the well-known path derived from the server
   URL (`/.well-known/oauth-protected-resource/<path>`, then the bare origin form) when the
   401 carries no challenge header. If the server publishes no document at all, its own
   origin is used as the authorization-server issuer.
2. **RFC 8414 authorization-server metadata** — tried for each advertised issuer in turn,
   over `/.well-known/oauth-authorization-server/<path>`, then the two OpenID Connect
   Discovery forms.
3. **RFC 7591 dynamic client registration** — fir registers itself as a public native client
   with the loopback redirect URI it is about to listen on. Registration is re-run on every
   interactive login because the loopback port is ephemeral (RFC 8252 §7.3); a stored
   `client_id` is reused only for refresh, which involves no redirect URI.
4. **Authorization code + PKCE (S256)** with the RFC 8707 `resource` indicator (the canonical
   MCP server URL) on the authorization, token and refresh requests.

Tokens are persisted in `auth.json` next to provider credentials, under an `mcp:<server>` key.
They survive a restart. A rotated refresh token is written back on every refresh.

### Logging in

fir **never opens a browser on its own** — structurally, not by policy. The MCP manager holds
no login-UI hook at all, so no reconnect cycle, background goroutine or non-interactive mode
can start a browser flow. (MCP servers connect from goroutines that may predate the terminal
UI and must not touch its widget tree; and the auto-reconnect loop re-dials forever, so an
automatic prompt there would be a browser-window storm.) A server that needs credentials fails
its connection with an actionable message naming the command to run:

```
MCP server "my-remote" requires OAuth authentication (no stored credential); run: fir mcp login my-remote
```

| Command | Context |
|---------|---------|
| `fir mcp list` | Show configured servers and their auth state |
| `fir mcp login <server>` | Run the OAuth flow from a plain terminal |
| `fir mcp logout <server>` | Delete stored credentials for a server |
| `/mcp login <server>` | Run the OAuth flow inside an interactive session |
| `/mcp logout <server>` | Delete stored credentials from inside a session |

The login flow races the loopback callback against a manual paste prompt, so it also works
over SSH where the browser cannot reach `127.0.0.1`.

`fir mcp login` probes the server unauthenticated first, so it picks up the `resource_metadata`
pointer from the 401 challenge (RFC 9728 §5.1) rather than having to guess the well-known path.

Non-interactive contexts — ACP mode, `-p`, CI — take the same path as everything else and fail
immediately with the message above rather than blocking on a browser that will never open.

### Transport security

fir refuses to send credentials over plaintext. A `sse`/`streamable` server whose URL is not
`https://` is rejected before any connection is made, unless the host is loopback
(`127.0.0.0/8`, `::1`, or `localhost`) so local development servers keep working. This applies
to OAuth bearers and static PATs alike: both ride the data path, and `http://mcp.internal:8080/mcp`
would put them on the wire in the clear. `oauthex` already enforces the same rule on the
discovery and token legs.

The escape hatch is `"auth": {"mode": "none"}`, which sends no credentials at all and is
therefore allowed over plain HTTP.

### Token lifecycle

- A token within 60 seconds of expiry is refreshed **before** the request is sent.
- A `401` on a live token triggers one silent refresh and a single replay of the request.
- When the refresh token is itself revoked or expired, the dead credential is deleted and the
  next connection reports that a login is required.
- The browser step runs **only** from `fir mcp login` / `/mcp login`. A failing server simply
  keeps reporting the actionable error on each reconnect cycle; it never escalates to a prompt.
- While a login is in flight, other requests to that server **fail fast** with
  `an interactive login is in progress` rather than queuing behind a human. Requests never
  ignore their own deadline waiting on the auth lock.
- A stored token is **bound to the server URL it was minted for**. `.fir/mcp.json` is
  project-local and therefore attacker-controllable: without the binding, a repo declaring
  `{"github": {"url": "https://evil.example/mcp"}}` would be handed the token fir holds for
  the real server. A URL mismatch degrades to "no token".

### The `auth` object

Every field is an escape hatch. Omit the whole object unless you need one.

| Field | Type | Description |
|-------|------|-------------|
| `mode` | string | `"oauth"` forces a login before the first request; `"bearer"` sends a static token and never attempts OAuth (inferred when `token` is set); `"none"` disables all auth handling so a 401 surfaces verbatim. Empty (the default) is the auto path described above |
| `token` | string | Static bearer token / PAT. A value of the form `${VAR}` or `$VAR` is read from the process environment, so the secret need not be written to disk. A literal token containing `$` is never expanded |
| `client_id` | string | Pre-registered OAuth client, for authorization servers without dynamic registration |
| `client_secret` | string | Accompanies `client_id` for confidential clients. Native apps normally have none. Supports `${VAR}` expansion |
| `scopes` | array of strings | Overrides the requested scope. Otherwise fir uses the challenge's `scope`, then the protected resource's `scopes_supported`, and otherwise omits the `scope` parameter entirely |
| `authorization_servers` | array of strings | Forces the OAuth issuer(s), for servers whose `/.well-known/oauth-protected-resource` document is absent, wrong or unreachable. **Replaces** the advertised `authorization_servers` list — it is not merged with it. Tried in order; the first issuer publishing usable RFC 8414 / OpenID Connect metadata wins. Each entry must be an `https` URL (plain `http` only for loopback) with no query or fragment. Only valid where the OAuth chain actually runs — the default mode or `"oauth"`; it is rejected with `"bearer"` and `"none"` |

When the metadata is wrong about the issuer it is often wrong about `scopes_supported` too, so
`authorization_servers` is usually paired with `scopes`. fir still *attempts* the
protected-resource fetch when issuers are forced — it is the only source of advertised scopes —
but a failure is no longer fatal.

Changing `authorization_servers` invalidates any stored token for that server: a credential
minted by an issuer no longer on the list is never reused, even across a restart. The stored row
is left in place, so putting the old issuer back recovers the credential without a fresh login.

```json
{
  "mcpServers": {
    "pat-server": {
      "transport": "streamable",
      "url": "https://a.example/mcp",
      "auth": {"token": "${MY_SERVER_PAT}"}
    },
    "no-dcr": {
      "transport": "streamable",
      "url": "https://b.example/mcp",
      "auth": {"client_id": "abc123", "scopes": ["mcp:read", "mcp:write"]}
    },
    "unauthenticated": {
      "transport": "streamable",
      "url": "https://c.example/mcp",
      "auth": {"mode": "none"}
    },
    "bad-metadata": {
      "transport": "streamable",
      "url": "https://d.example/mcp",
      "auth": {
        "authorization_servers": ["https://login.d.example"],
        "scopes": ["mcp:read"]
      }
    }
  }
}
```

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

## Tool-call timeout

Every MCP tool call the model dispatches is bounded by a default wall-clock timeout so an
unresponsive server cannot hang the whole turn. When the deadline is hit the underlying
`tools/call` is genuinely cancelled (the context is a child of the turn context, so the
cancellation propagates into the MCP client and unwinds the round-trip) and the model receives a
clean, actionable result — `MCP tool "mcp__srv__x" timed out after 2m0s …` — rather than a raw
context error.

- **Default:** 120 seconds. (The MCP ecosystem norm is 60s; fir is deliberately more generous so
  legitimately slow tools — browser automation, large fetches/queries — are not clipped.)
- **Configure** via `settings.json` (global or project):

  ```json
  { "mcp": { "toolTimeoutSeconds": 120 } }
  ```

  or the `FIR_MCP_TOOL_TIMEOUT` environment variable (seconds), which takes precedence. A value
  `<= 0` disables the bound entirely (a call then runs until it finishes or the turn is cancelled).
- **Scope:** only tool calls the model dispatches directly are bounded. Calls issued *through* a
  built-in or extension — `pipe`, `wait`, `aside`, or any extension calling a tool — are governed
  by that caller's own timeout, so a `pipe`/`wait` step declaring `timeout=-1` can still run a slow
  MCP tool arbitrarily long.
- The bound is on wall-clock time, not silence: a tool that streams progress notifications is still
  cut off at the deadline.

When MCP tools are present, the system prompt tells the model the default exists so it can react to
a timeout (retry, take another approach, or ask the user to raise the setting).

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

- **Login is user-initiated, never automatic.** fir performs the full OAuth discovery and
  token lifecycle on its own (see [Authentication](#authentication-oauth)), but the browser
  step must be started explicitly with `fir mcp login <server>` or `/mcp login <server>`.
  Stdio servers still take credentials via `env`.
- **Error isolation is per-server.** If one MCP server fails to start, the entire `session/new`
  request is rejected. Servers that exit mid-session cause subsequent tool calls to that server
  to return errors; other servers and built-in tools remain unaffected.
