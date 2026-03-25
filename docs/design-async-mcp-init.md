# Async MCP Initialization

**Date:** 2025-03-25  
**Status:** Draft  
**Problem:** A single slow or bloated MCP server blocks session creation for all servers and the entire UI.

## Background

The VictoriaMetrics MCP server returns 6,773 resources in `resources/list` (8.6 MB). During `Start()`, fir iterates all resources and issues a `Subscribe()` RPC for each one — 6,773 sequential round-trips. This blocks session creation for ~11 minutes.

But the deeper issue is architectural: `Manager.Start()` is fully synchronous. Every server's handshake, tool listing, and resource subscription must complete before the user can type a single character.

## Goals

1. Session becomes interactive immediately — MCP tools appear as servers finish connecting.
2. A slow or broken MCP server never blocks other servers or the session.
3. Resource subscriptions don't happen at startup at all — subscribe lazily on first read.
4. No per-server configuration or hard-coded workarounds.

## Design

### Change 1: Lazy resource subscriptions

**File:** `pkg/mcp/client.go` — `startServer()`

Remove the resource subscription loop from `startServer()`. Instead, subscribe to a resource the first time it's read via `readResourceTool`.

**Before (startup):**
```go
if caps != nil && caps.Resources != nil {
    for res, err := range session.Resources(ctx, nil) {
        // ... record URI, subscribe
        _ = session.Subscribe(ctx, &sdk.SubscribeParams{URI: res.URI})
    }
}
```

**After (startup):** Delete the loop entirely.

**File:** `pkg/mcp/resources.go` — `readResourceTool()`

After a successful `ReadResource`, subscribe to the URI if not already subscribed. The Manager needs to expose a method for this, or `readResourceTool` takes a subscribe callback.

```go
Execute: func(ctx context.Context, _ string, params map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
    // ... read resource ...
    result, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
    if err != nil {
        return agent.AgentToolResult{}, err
    }
    // Subscribe lazily — best-effort, non-blocking.
    go subscribeOnce(ctx, session, serverName, uri)
    return convertResourceResult(result), nil
}
```

The `subscribeOnce` function checks the `subscribed` map and only subscribes if this URI hasn't been seen before. Same mutex pattern as today, just triggered lazily.

**Also update:** the `tools/list_changed` notification handler (line ~268) has the same pattern — it re-lists all resources and subscribes to new ones. Apply the same treatment: skip the subscription loop, let reads trigger subscriptions.

### Change 2: Async server startup

**File:** `pkg/mcp/client.go` — `Start()`

Today `Start()` loops over servers sequentially and returns all tools or the first error:

```go
func (m *Manager) Start(ctx context.Context) ([]agent.AgentTool, error) {
    for name, cfg := range m.configs {
        tools, err := m.startServer(ctx, name, cfg)
        if err != nil { return nil, err }  // one failure kills everything
        allTools = append(allTools, tools...)
    }
    return allTools, nil
}
```

Change to:

```go
func (m *Manager) Start(ctx context.Context) {
    for name, cfg := range m.configs {
        go m.startServerAsync(ctx, name, cfg)
    }
}
```

`Start()` returns immediately with no tools and no error. Each server connects in its own goroutine. On success, the goroutine stores tools in `m.tools[name]` and calls `m.OnToolsChanged(m.allTools())`. On failure, it records the error in `m.serverErrors[name]` and logs a warning — no different from what happens today with config-watch reconnections.

This is consistent with how the post-startup code already works: `tools/list_changed` notifications, config file watch reloads, and disconnect/reconnect all update tools via `OnToolsChanged` from background goroutines.

**File:** `pkg/session/factory.go`

Today:
```go
mcpMgr = mcp.NewManager(opts.MCPConfigs, false)
mcpTools, err := mcpMgr.Start(ctx)       // blocks
toolList = append(toolList, mcpTools...)   // pass to agent
```

After:
```go
mcpMgr = mcp.NewManager(opts.MCPConfigs, false)

// Create the agent session with base tools only.
result, err := CreateAgentSession(ctx, CreateAgentSessionOptions{
    Tools: toolList,  // no MCP tools yet
    // ...
})

// Wire up the callback: when MCP tools arrive, merge with base tools and push to agent.
baseTools := toolList
mcpMgr.OnToolsChanged = func(mcpTools []agent.AgentTool) {
    merged := append(slices.Clone(baseTools), mcpTools...)
    result.Session.Agent.SetTools(merged)
}

// Start is non-blocking now.
mcpMgr.Start(ctx)
```

`Agent.SetTools()` is already mutex-protected, so this is safe. The agent's tool list grows as servers come online.

### Change 3: Wire `OnToolsChanged` (existing but unused)

`Manager.OnToolsChanged` was designed for exactly this purpose but was never connected in production code. The factory change above wires it. This also means that post-startup events (server reconnections, config file changes) now correctly update the agent's tools — a latent bug fix.

**Note:** `SetTools` replaces the full list, so the callback must merge base tools + all MCP tools, not just append. The `allTools()` helper already aggregates across all servers; the factory just needs to prepend the base tools.

### What about hooks?

`AgentSession.SetHooks()` wraps tools with hook interception. If tools arrive after hooks are set, the new tools won't be wrapped. Two options:

1. Have `OnToolsChanged` go through `SetHooks`-aware path (call `WrapToolsWithHooks` before `SetTools`).
2. Make `SetTools` always apply current hooks.

Option 2 is cleaner — `SetTools` should be the single funnel. But this is a small follow-up; the wrapping only matters for extensions that use `OnToolCall`/`OnToolResult` hooks.

## Testing

1. **Unit test for lazy subscribe:** Start a server with resources, verify no `Subscribe` calls at startup. Call `read_resource`, verify `Subscribe` is called for that URI only.
2. **Unit test for async start:** Start a manager with two servers (one slow, one fast). Verify `Start()` returns immediately. Verify `OnToolsChanged` fires for the fast server first, then the slow one.
3. **E2E:** Connect to a server with many resources. Verify session starts in <2s regardless of resource count.

## Rollout risk

Low. The async patterns already exist in the codebase for post-startup events. We're making startup behave the same way. The lazy subscribe is purely additive — we subscribe to fewer things, later.

The one behavioral change users might notice: MCP tools won't appear in the first LLM request if the server hasn't finished connecting yet. In practice, `tools/list` is fast for well-behaved servers (sub-second), so tools will almost always be available by the time the user finishes typing their first message. For slow servers, tools appearing a few seconds late is strictly better than the session being frozen for minutes.
