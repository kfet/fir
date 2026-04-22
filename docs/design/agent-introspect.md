# Design: `agent-introspect` extension

Related: `docs/extension-protocol.md`

## Goal

Give the agent one tool that returns structured info about the current
runtime: session, model, context usage, and runtime stats.

- Ships as an **extension**, not a Go tool.
- Single source of truth in Go; extension is a thin wrapper.
- `/session` output is untouched.

## Shape

```text
LLM ──tool call──► agent-introspect ext ──agent.info RPC──► fir host ──► AgentSession.Introspect()
```

1. **Go builder** `pkg/session/agentsession.go` — `Introspect()` returning
   `session.Introspection`.
2. **Host RPC** `agent.info` — served per-session bridge, no params,
   returns the `Introspection` JSON.
3. **Extension** `pkg/resources/builtin_extensions/agent_introspect.py` — one tool
   `agent_introspect` that returns `json.dumps(ctx.agent_info())`.

## Data (v1)

```json
{
  "version": "…",
  "mode": "interactive | acp | print",
  "cwd": "/abs/path",
  "session":  { "id": "…", "file": "…", "name": "…" },
  "model":    { "id": "…", "provider": "…", "contextWindow": 200000 },
  "context":  { "tokens": 12345, "window": 200000, "percent": 6.2,
                "compactMode": "off|client|server" },
  "thinking": { "current": "medium", "available": ["off", "…"] },
  "messages": { "user": 0, "assistant": 0, "toolCalls": 0, "toolResults": 0, "total": 0 },
  "tokens":   { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0 },
  "cost":     0.0
}
```

Conventions for unknown values: `""` / `0` / `[]` / `{}`, except
`context.tokens` and `context.percent` use `-1` when genuinely unknown
(e.g. right after compaction).

All fields reuse existing helpers: `GetSessionStats`, `GetContextUsage`,
`CompactMode`, `GetAvailableThinkingLevels`.

## Per-session semantics

Extensions are **per session**. `extension.Setup` runs once per session
(once in interactive; once per `session/new` in ACP), spawning extension
processes wired to that session's bridge.

Therefore `agent_introspect` returns data for the calling session only.
With N concurrent ACP sessions there are N extension processes, each
bound to its own `AgentSession`. No session ID is needed in
`agent.info` params — the bridge binds it. No cross-session leakage is
possible.

## Non-goals (v1)

- No refactor of `/session` rendering.
- No section filters, formats, diffs, or subscriptions.
- No env-var fan-out.
