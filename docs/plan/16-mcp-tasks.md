# 16 — MCP Support: Implementation Tasks

Reference: `docs/plan/16-mcp-support.md`

---

## Task 1: Add SDK dependency

**Files:** `go.mod`, `go.sum`

```bash
go get github.com/modelcontextprotocol/go-sdk@latest
```

**Test:** `go mod tidy && make test` (no code changes, just dependency)

**Commit:** `build: add MCP Go SDK dependency`

---

## Task 2: Config types — `pkg/mcp/config.go`

**File:** `pkg/mcp/config.go` (~40 lines)

Define:

```go
package mcp

type ServerConfig struct {
    Command string            `json:"command"`
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

// ConfigFile is the top-level structure of .fir/mcp.json.
type ConfigFile struct {
    MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfigFile reads and parses a .fir/mcp.json file.
func LoadConfigFile(path string) (*ConfigFile, error)
```

**Test file:** `pkg/mcp/config_test.go` (~40 lines)
- Round-trip JSON marshal/unmarshal
- LoadConfigFile with a temp file
- Zero-value / empty configs

**Commit:** `feat(mcp): add server config types`

---

## Task 3: Client manager — `pkg/mcp/client.go`

**File:** `pkg/mcp/client.go` (~120 lines)

Implement `Manager`:

```go
type Manager struct { ... }

func NewManager(configs map[string]ServerConfig) *Manager
func (m *Manager) Start(ctx context.Context) ([]agent.AgentTool, error)
func (m *Manager) Close() error
```

`Start` for each config entry:
1. Build `cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)` with env from `cfg.Env`
2. `transport := &mcp.CommandTransport{Command: cmd}`
3. `client := mcp.NewClient(&mcp.Implementation{Name: "fir", Version: "..."}, nil)`
4. `session, err := client.Connect(ctx, transport, nil)`
5. `result, err := session.ListTools(ctx, nil)`
6. For each tool in `result.Tools`, call `AdaptTool(session, serverName, tool)`
6. Store session for later `Close()`

`Close` iterates sessions, calls `session.Close()`.

**Test file:** `pkg/mcp/client_test.go` (~80 lines)
- Spin up an in-process MCP server using `mcp.NewServer(...)` + `mcp.AddTool`
- Use `mcp.NewInMemoryTransports()` to get paired client/server transports
- Verify tools are listed and callable
- Verify `Close` is clean

**Commit:** `feat(mcp): implement client manager`

---

## Task 4: Tool adapter — `pkg/mcp/tool_adapter.go`

**File:** `pkg/mcp/tool_adapter.go` (~80 lines)

```go
func AdaptTool(session *mcp.ClientSession, serverName string, tool mcp.Tool) agent.AgentTool
func convertResult(result *mcp.CallToolResult) agent.AgentToolResult
```

Key behaviors:
- Tool name: `mcp__<serverName>__<tool.Name>` (double-underscore separator, safe for LLM tool calling)
- Label: `tool.Title` or fallback to `tool.Name` + ` (via <serverName>)`
- Parameters: `tool.InputSchema` passed through as `ai.Tool.Parameters`
- Execute: calls `session.CallTool`, type-switches `mcp.Content` (interface) → `ai.ToolResultContent`
  - `*mcp.TextContent` → `ai.ToolResultContent{Type: "text", Text: c.Text}`
  - `*mcp.ImageContent` → `ai.ToolResultContent{Type: "image", ...}`
  - If `result.IsError` (bool) is true, wrap as error content
- Context cancellation propagates to `CallTool`

**Test file:** `pkg/mcp/tool_adapter_test.go` (~60 lines)
- Verify name prefixing
- Verify parameter pass-through
- Verify result conversion (text, error cases)
- Verify cancellation

**Commit:** `feat(mcp): implement tool adapter`

---

## Task 5: Wire into agent startup

**Files changed:**
- `pkg/modes/acp/acp.go` (~15 lines added in `createSession`)
- `pkg/modes/acp/acp.go` — add `mcpManager` field to `firSession` struct

In `createSession`, after building `toolList`:

```go
if len(mcpConfigs) > 0 {
    mcpMgr := mcp.NewManager(mcpConfigs)
    mcpTools, err := mcpMgr.Start(ctx)
    if err != nil {
        return nil, fmt.Errorf("start MCP servers: %w", err)
    }
    toolList = append(toolList, mcpTools...)
    entry.mcpManager = mcpMgr
}
```

On session close, call `entry.mcpManager.Close()`.

For now, `mcpConfigs` comes from a project-level `.fir/mcp.json` if present.
Use `mcp.LoadConfigFile(".fir/mcp.json")` to load it.

**Test:** Existing tests still pass. Integration test deferred to task 6.

**Commit:** `feat(mcp): wire MCP tools into session startup`

---

## Task 6: ACP injection — `pkg/mcp/acp_inject.go`

**File:** `pkg/modes/acp/types.go` (~10 lines added)

Add to local types:

```go
type NewSessionRequestExt struct {
    acpsdk.NewSessionRequest
    McpServers map[string]mcp.ServerConfig `json:"mcpServers,omitempty"`
}
```

**File:** `pkg/modes/acp/conn.go` (~5 lines changed)

In `case "session/new"`:
- Unmarshal into `NewSessionRequestExt` instead of `acpsdk.NewSessionRequest`
- Pass `p.McpServers` into `NewSession` / `createSession`

**File:** `pkg/modes/acp/acp.go` (~5 lines changed)

- `NewSession` signature gains `mcpServers map[string]mcp.ServerConfig`
- Forwards to `createSession`

**Test:** Add test in `pkg/modes/acp/acp_test.go`:
- Send `session/new` with `mcpServers` field
- Verify tools from MCP server appear in session

**Commit:** `feat(mcp): accept MCP server configs in ACP session/new`

---

## Dependency Graph

```
Task 1 (go get)
  └─ Task 2 (config types)
       └─ Task 4 (tool adapter)
       └─ Task 3 (client manager) ← depends on Task 4
            └─ Task 5 (wiring)
                 └─ Task 6 (ACP injection)
```

Tasks 2 and 4 can be done in parallel after Task 1. Task 3 depends on 4 since
the manager calls `AdaptTool`. Tasks 5 and 6 are sequential.
