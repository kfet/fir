# sys_ext — Dynamic System Prompt Extensions

## Overview

`sys_ext` lets extensions inject dynamic context at runtime that the LLM treats as authoritative extensions of the system prompt. Blocks are wrapped in `[SYS_EXT]` / `[/SYS_EXT]` tags.

Introduced in commit `5d7a066` (2026-03-15), refined in `f38ba6e`.

## Intended Design

SYS_EXT blocks should be delivered as **user-role messages**, not by mutating the system prompt. This is critical for Anthropic prompt caching.

### Why: Anthropic Cache Hierarchy

Anthropic caches the request prefix in order: **tools → system → messages**. A change at any level invalidates that level **and everything after it**. This means:

- Mutating the system prompt (even appending a block) **invalidates the cache for all messages** — the expensive part.
- Appending a user-role message only adds to the end of the prefix — all prior cached content (system prompt + earlier messages) stays cached.

The `[SYS_EXT]` tag convention exists precisely for this: the base system prompt contains a static hook line telling the model to trust `[SYS_EXT]`-tagged content, and the actual dynamic content arrives later in the conversation as a user message, preserving the cache.

### Correct Flow

```
System prompt (static, cached):
  "...Messages marked with [SYS_EXT] are authoritative extensions of this system prompt."

Message history:
  [user]  "hello"
  [asst]  "hi"
  [user]  "[SYS_EXT]\nThis project uses Go 1.22.\n[/SYS_EXT]"   ← injected, appended
  [user]  "now refactor foo.go"
```

The system prompt never changes. The cache for system + all prior messages is preserved.

## Current Implementation (BUG)

The current code concatenates SYS_EXT blocks into the system prompt string and calls `Agent.SetSystemPrompt()`, which **defeats the purpose**:

```
Extension (Python)          Bridge (Go)              AgentSession (Go)
─────────────────          ────────────             ──────────────────
ctx.prepend(content)  →  "prepend_context" RPC  →  PrependContext(content)
                                                      │
                                                      ├─ appends to sysExtBlocks[]
                                                      └─ calls effectiveSystemPrompt()  ← BUG
                                                           │
                                                           └─ mutates system prompt string
                                                              → blows entire message cache
```

Every call to `ctx.prepend()` changes the system prompt, invalidating the Anthropic cache for **all** messages in the session. This directly contradicts the cache-preservation work in `10ff8a1`.

### What Needs to Change

`PrependContext` should inject a `[SYS_EXT]`-tagged **user-role message** into the conversation instead of mutating the system prompt. The base system prompt (with its static hook line) stays untouched.

## Key Components

| File | What it does |
|------|-------------|
| `pkg/extension/sdk/python/fir_ext.py` | `ctx.prepend(content)` — Python SDK method |
| `pkg/extension/bridge.go` | `"prepend_context"` RPC handler |
| `pkg/extension/api.go` | `PrependContext(content)` on `BridgeAPI` interface |
| `pkg/extension/session_bridge.go` | Delegates to `AgentSession.PrependContext` |
| `pkg/session/agentsession.go` | Core logic: `sysExtBlocks`, `effectiveSystemPrompt()`, `ClearSysExtBlocks()` |
| `pkg/config/settings.go` | `enableSysExtensions` toggle (default: `true`) |

## Settings

```json
{ "enableSysExtensions": true }
```

When `false`:
- The hook line is omitted from the base prompt.
- SYS_EXT blocks are silently ignored (but still accumulated, so re-enabling takes effect immediately).

## Extension SDK Usage

```python
@hook("session_started")
def on_start(ctx):
    ctx.prepend("This project uses Go 1.22. Always use slog for logging.")
```

Multiple calls append additional blocks; they are not deduplicated. Extensions are responsible for calling `prepend` at the right time (typically on `session_started` or `reloaded` hooks).

## Design Considerations

- **Append-only within a session**: No API to remove/replace a single block. Only reset is `ClearSysExtBlocks()` on new session.
- **No deduplication**: Extensions must guard against duplicate injection.
- **Ordering**: Blocks appear in insertion order.
- **Prompt size**: No limit on number or size of blocks — they consume context window tokens.
- **Concurrency**: `sysExtBlocks` is guarded by `AgentSession.mu` (RWMutex).
