# Architecture

## Layer Diagram

```
┌──────────────────────────────────────────────┐
│  cmd/pi/main.go                               │  CLI entry, arg parsing
├──────────────────────────────────────────────┤
│  pkg/modes/ (interactive | print | rpc)       │  UI layer
├──────────────────────────────────────────────┤
│  pkg/core/agentsession.go                     │  Session lifecycle, compaction, retry
├──────────────────────────────────────────────┤
│  pkg/core/sdk.go                              │  Factory: wires Agent + Session + Tools
├──────────────────────────────────────────────┤
│  pkg/core/session.go                          │  JSONL persistence, branching
├──────────────────────────────────────────────┤
│  pkg/core/tools/                              │  read, bash, edit, write, grep, find, ls
├──────────────────────────────────────────────┤
│  pkg/agent/                                   │  Agent loop: stream → tool exec → repeat
├──────────────────────────────────────────────┤
│  pkg/ai/                                      │  LLM providers, SSE streaming, OAuth
├──────────────────────────────────────────────┤
│  pkg/tui/                                     │  Terminal rendering, components
└──────────────────────────────────────────────┘
```

## Key TS→Go Translations

| TypeScript | Go |
|---|---|
| `async/await` | goroutines + channels |
| `AbortSignal` | `context.Context` |
| `AsyncIterable<Event>` | `chan Event` |
| `subscribe(fn)` | callback slice or channel fan-out |
| Union types (`Message`) | interfaces + type switches |
| TypeBox schemas | Go structs with JSON tags |
| `class Foo` | `type Foo struct` + methods |
| `import { x } from "./y.js"` | `import "pkg/y"` |

## Extension System

**Deferred to v2.** The TS extension system dynamically loads JS/TS via `jiti` (~2100 lines). Not feasible on Pi Zero.

Design: keep hook call sites in `AgentSession` (no-op), add process-based extensions (JSON-RPC) later.
