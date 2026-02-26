# 16 — MCP (Model Context Protocol) Support

## Overview

Add support for connecting to external MCP servers so their tools become
available to the LLM alongside fir's built-in tools. MCP servers are declared
in config and started as stdio subprocesses.

## SDK Choice

**Module:** `github.com/modelcontextprotocol/go-sdk` v1.3.1+

```bash
go get github.com/modelcontextprotocol/go-sdk@latest
```

Key types used:
- `mcp.Client` / `mcp.ClientSession` — connect, list tools, call tools
- `mcp.CommandTransport` — stdio subprocess transport; wraps `*exec.Cmd`
- `mcp.Tool` — server-side tool definition (Name, Description, InputSchema)
- `mcp.CallToolParams` / `mcp.CallToolResult` — tool invocation
- `mcp.Content` — **interface** with concrete types `*mcp.TextContent`,
  `*mcp.ImageContent`, etc.
- `mcp.NewInMemoryTransports()` — paired in-process transports for testing

## Package Layout

All new code goes in `pkg/mcp/`:

| File | Purpose | ~Lines |
|---|---|---|
| `config.go` | `MCPServerConfig` type + YAML parsing | 40 |
| `client.go` | `Manager` — lifecycle of MCP client sessions | 120 |
| `tool_adapter.go` | Convert `mcp.Tool` → `agent.AgentTool` | 80 |
| `config_test.go` | Config parsing tests | 40 |
| `client_test.go` | Manager tests with fake MCP server | 80 |
| `tool_adapter_test.go` | Adapter unit tests | 60 |

Plus wiring changes in existing files (~30 lines total).

## Config Schema

In `~/.fir/mcp.json` or `.fir/mcp.json` (project-level):

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {
        "NODE_PATH": "/usr/local/lib/node_modules"
      }
    },
    "database": {
      "command": "./my-db-server",
      "args": ["--port", "5432"]
    }
  }
}
```

Go types:

```go
// ServerConfig describes a single MCP server to launch.
type ServerConfig struct {
    Command string            `json:"command"`
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

// ConfigFile is the top-level structure of .fir/mcp.json.
type ConfigFile struct {
    MCPServers map[string]ServerConfig `json:"mcpServers"`
}
```

## fir Tool Interface

fir tools are `agent.AgentTool`:

```go
type AgentTool struct {
    ai.Tool  // Name, Description, Parameters (JSON Schema)
    Label    string
    Execute  func(ctx context.Context, toolCallID string,
                  params map[string]any,
                  onUpdate AgentToolUpdateCallback) (AgentToolResult, error)
}
```

## Tool Adapter: MCP → fir

`MCPToolAdapter` converts an `mcp.Tool` + active `*mcp.ClientSession` into an
`agent.AgentTool`:

```go
func AdaptTool(session *mcp.ClientSession, tool mcp.Tool) agent.AgentTool {
    return agent.AgentTool{
        Tool: ai.Tool{
            Name:        tool.Name,
            Description: tool.Description,
            Parameters:  tool.InputSchema, // already a JSON-Schema-like any
        },
        Label: tool.Title, // or tool.Name if empty
        Execute: func(ctx context.Context, toolCallID string,
            params map[string]any, onUpdate agent.AgentToolUpdateCallback,
        ) (agent.AgentToolResult, error) {
            result, err := session.CallTool(ctx, &mcp.CallToolParams{
                Name:      tool.Name,
                Arguments: params,
            })
            if err != nil {
                return agent.AgentToolResult{}, err
            }
            return convertResult(result), nil
        },
    }
}
```

`convertResult` type-switches on `mcp.Content` (an interface):
- `*mcp.TextContent` → `ai.ToolResultContent{Type: "text", Text: c.Text}`
- `*mcp.ImageContent` → `ai.ToolResultContent{Type: "image", ...}`
- If `result.IsError` (bool) is true, mark as error content.

## Manager

`Manager` owns the lifecycle of all MCP client sessions for one fir session:

```go
type Manager struct {
    configs  map[string]MCPServerConfig
    sessions map[string]*mcp.ClientSession
}

func NewManager(configs map[string]MCPServerConfig) *Manager
func (m *Manager) Start(ctx context.Context) ([]agent.AgentTool, error)
func (m *Manager) Close() error
```

`Start` iterates configs, builds `*exec.Cmd` from each config, wraps in
`mcp.CommandTransport{Command: cmd}`, connects via
`mcp.NewClient(&mcp.Implementation{Name:"fir",Version:"..."}, nil).Connect(ctx, transport, nil)`,
calls `session.ListTools(ctx, nil)`, adapts each tool (prefixing the tool name
with `mcp__<serverName>__<toolName>` to avoid collisions), and returns the
aggregate tool list.

## Wiring Into Agent Startup

In `pkg/modes/acp/acp.go`, `createSession`:

1. Load MCP server configs (from session-level or global config).
2. Create `mcp.NewManager(configs)`.
3. Call `manager.Start(ctx)` to get `[]agent.AgentTool`.
4. Append MCP tools to `toolList` before passing to agent loop.
5. Store manager on the session entry for cleanup on session close.

For non-ACP (interactive) mode, the same wiring happens in the interactive
session setup path.

## ACP Injection

The ACP `session/new` request (`acpsdk.NewSessionRequest`) can carry MCP
server configs from the client (e.g., VS Code). We extend the local request
type:

```go
// In pkg/modes/acp/types.go, extend or shadow NewSessionRequest:
type NewSessionRequestExt struct {
    acpsdk.NewSessionRequest
    McpServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
}
```

In `conn.go` case `"session/new"`, unmarshal into `NewSessionRequestExt`
instead, and pass `McpServers` through to `createSession`.

## Implementation Phases

1. **Dependency** — `go get`, commit go.mod/go.sum
2. **Config types** — `pkg/mcp/config.go` + test
3. **Client manager** — `pkg/mcp/client.go` + test with in-process MCP server
4. **Tool adapter** — `pkg/mcp/tool_adapter.go` + test
5. **Agent wiring** — integrate into session creation, load from config files
6. **ACP injection** — accept `mcpServers` in `session/new`, forward to manager
