# ACP RFD: Authentication Methods

> Source: https://agentclientprotocol.com/rfds/auth-methods
> Raw: https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/main/docs/rfds/auth-methods.mdx
> Reference impl: https://github.com/zed-industries/claude-agent-acp/blob/main/src/acp-agent.ts
> Last synced: 2026-03-02

## Auth Method Types

### 1. Agent auth (default)

Agent handles auth itself. Default type if none specified.

```json
{
  "id": "123",
  "name": "Agent",
  "description": "Authenticate through agent",
  "type": "agent"
}
```

### 2. Env variable

Client collects a key and passes it as an env var. Client may restart the agent process with the env var set, then automatically send the authenticate message.

```json
{
  "id": "123",
  "name": "OpenAI api key",
  "description": "Provide your key",
  "type": "env_var",
  "varName": "OPEN_AI_KEY",
  "link": "OPTIONAL link to a page where user can get their key"
}
```

### 3. Terminal auth (RFD spec)

Client runs the **same binary** in an interactive terminal with additional args/env.

```json
{
  "id": "123",
  "name": "Run in terminal",
  "description": "Setup Label",
  "type": "terminal",
  "args": ["--setup"],
  "env": { "VAR1": "value1", "VAR2": "value2" }
}
```

### 3b. Terminal auth (Zed/Claude Agent pattern)

Zed uses `_meta["terminal-auth"]` instead of the RFD's `type: "terminal"`. The client
advertises support via `clientCapabilities._meta["terminal-auth"] = true` in the
initialize request. The agent then populates `_meta["terminal-auth"]` on auth methods:

```json
{
  "id": "oauth-anthropic",
  "name": "Login with Anthropic",
  "description": "Login with Anthropic via OAuth",
  "_meta": {
    "terminal-auth": {
      "command": "/path/to/fir",
      "args": ["--login", "anthropic"],
      "label": "Anthropic Login"
    }
  }
}
```

### AuthErrors

When a session/prompt fails due to missing auth, the error can include relevant `authMethods`:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "error": {
    "code": -32000,
    "message": "Authentication required",
    "authMethods": [...]
  }
}
```
