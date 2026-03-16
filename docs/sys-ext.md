# sys_ext — Dynamic System Prompt Extensions

## Overview

`sys_ext` lets extensions inject dynamic context at runtime that the LLM treats as authoritative extensions of the system prompt. Blocks are wrapped in `[SYS_EXT]` / `[/SYS_EXT]` tags.

Introduced in commit `5d7a066` (2026-03-15), refined in `f38ba6e`.

## Anthropic Cache Hierarchy

Anthropic caches the request prefix in order: **tools → system → messages**. A change at any level invalidates that level **and everything after it**.

| Mutated layer | What gets invalidated |
|---------------|----------------------|
| Tools | Tools + system + all messages |
| System prompt | System + all messages |
| Append message | Nothing (new content at the end) |

### Current cache safety of each layer

- **Tools**: Safe. Extensions register tools at init/handshake only. `UnregisterExtensionTools` + `RegisterTools` only runs on `/reload`, which already invalidates everything. No mid-session tool mutation.
- **System prompt**: **Broken by sys_ext** (see below). Otherwise stable — date is frozen per session (`10ff8a1`), prompt only rebuilt on explicit `/reload`.
- **Messages**: Safe. Deterministic synthetic tool-result timestamps (`10ff8a1`), block-form user messages for `cache_control` attachment. PrefixGuard detects regressions.

## Intended Design

SYS_EXT content should be delivered as **user-role messages**, not by mutating the system prompt:

```
System prompt (static, cached):
  "...Messages marked with [SYS_EXT] are authoritative extensions of this system prompt."

Message history:
  [user]  "hello"
  [asst]  "hi"
  [user]  "[SYS_EXT]\nThis project uses Go 1.22.\n[/SYS_EXT]"   ← appended, cache-safe
  [user]  "now refactor foo.go"
```

The system prompt never changes. The cache for system + all prior messages is preserved.

## Implementation

`PrependContext` injects a `[SYS_EXT]`-tagged **user-role message** into the conversation. The base system prompt contains a static hook line and is never mutated, preserving the Anthropic prompt cache.

```
ctx.prepend(content)  →  "prepend_context" RPC  →  PrependContext(content)
                                                      │
                                                      └─ Agent.AppendMessage(user: "[SYS_EXT]\n...\n[/SYS_EXT]")
                                                         → appended to conversation, cache intact
```

When `enableSysExtensions` is `false`, `PrependContext` is a no-op and the hook line is omitted from the system prompt.

## Scope: Internal Only (Skills on /reload)

The only legitimate use case today is **skills injecting context into the system prompt during `/reload`**. At that point the cache is already invalidated (tool list changes blow everything), so mutating the system prompt is harmless.

For mid-session dynamic context (if ever needed), the correct approach is user-role message injection — a different API that doesn't exist yet.

**Example future use case**: date rollover. The date is frozen per session (`10ff8a1`) to avoid cache-busting, but sessions spanning midnight have a stale date. A `[SYS_EXT]` user-role message like "Current date is now 2026-03-17" would fix it without touching the system prompt. Low priority (niche, low-stakes) but a clean example of the pattern.

### Extension API Should Be Removed

The following surface area was added prematurely and invites cache-busting misuse:

| Component | Action |
|-----------|--------|
| `ctx.prepend()` in `fir_ext.py` | Remove |
| `"prepend_context"` RPC in `bridge.go` | Remove |
| `PrependContext` on `BridgeAPI` interface | Remove from interface |
| `demo.py` usage | Remove |

`AgentSession.PrependContext()` can remain as an internal method for the skill/reload code path.

## Key Components

| File | What it does |
|------|-------------|
| `pkg/config/settings.go` | `enableSysExtensions` toggle (default: `true`) |
| `pkg/session/agentsession.go` | Core: `sysExtBlocks`, `effectiveSystemPrompt()`, `ClearSysExtBlocks()` |
| `pkg/extension/session_bridge.go` | Delegates to `AgentSession.PrependContext` |
| `pkg/extension/bridge.go` | `"prepend_context"` RPC handler (to be removed) |
| `pkg/extension/api.go` | `PrependContext` on `BridgeAPI` interface (to be removed) |
| `pkg/extension/sdk/python/fir_ext.py` | `ctx.prepend()` (to be removed) |

## Design Considerations

- **Append-only within a session**: No API to remove/replace a single block. Only reset is `ClearSysExtBlocks()` on new session.
- **No deduplication**: Callers must guard against duplicate injection.
- **Ordering**: Blocks appear in insertion order.
- **Prompt size**: No limit on number or size of blocks — they consume context window tokens.
- **Concurrency**: `sysExtBlocks` is guarded by `AgentSession.mu` (RWMutex).
