# Refactor Plan: Reusable Go Module Reorganization

Goal: break the monolithic `pkg/ai` and grab-bag `pkg/core` into focused
sub-packages, rename `pkg/core` → `pkg/session` and move persistence to
`pkg/session/store`, and fix the `tui` layering issue.

## Principles

- Sub-packages over new top-level packages. Keep `pkg/` scannable.
- Every package has one clear responsibility.
- Lower packages (types, agent) stay slim so they're reusable.

---

## Phase 1 — Split `pkg/ai` into sub-packages

Status: **done**

Moved truly independent files into sub-packages:
- `pkg/ai/jsonparse` — streaming JSON repair/parse
- `pkg/ai/ratelimit` — rate limit detection and parsing
- `pkg/ai/overflow` — context overflow detection
- `pkg/ai/envkeys` — provider API key resolution from env vars

Note: `stream.go`, `models.go`, `registry.go`, `eventstream.go` stay in
`pkg/ai` due to mutual dependencies (Model ↔ StreamFunction ↔ Registry).

---

## Phase 2 — Rename `pkg/core` → `pkg/session`, extract utilities

Status: **done**

- `pkg/core` → `pkg/session` (AgentSession orchestration)
- `pkg/session` (old persistence) → `pkg/session/store`
- `pkg/core/compaction` → `pkg/compaction` (top-level, avoids circular dep with session)
- `bashexec.go` → `pkg/exec/` (bash execution isn't a session concern)
- `clipboardimage.go` → `pkg/resources/clipboard/` (image reading from OS clipboard)

Note: `export.go` and `sdk.go` stay in `pkg/session` — they are methods
on `*AgentSession` and can't move without creating circular deps.

---

## Phase 3 — Extract `pkg/app` (shared mode bootstrap)

Status: **deferred**

Bootstrap duplication between `cmd/fir/app.go` and `pkg/modes/acp/acp.go`
is real but moderate (~30 lines). Worth revisiting if a third mode is added
or the bootstrap grows more complex.

---

## Phase 4 — Clean up `pkg/resources`

Status: **deferred**

Skills, extensions, and resource loading are coupled through the
`ResourceLoader` interface. Splitting into sub-packages would require
refactoring the interface, with limited benefit at current scale.

---

## Phase 5 — Fix `tui` ↔ `modes/interactive` layering

Status: **done**

Moved 4 test files from `pkg/tui/` to `pkg/modes/interactive/components/`
to eliminate the upward test-time dependency. `pkg/tui` no longer imports
anything from `pkg/modes/`.

---

## Current `pkg/` layout

```
pkg/
  agent/              Runs the tool-use loop (prompt → LLM → tools → repeat)
    tools/            Built-in tool implementations
  ai/                 Core AI types + model registry + streaming
    envkeys/          Provider API key resolution from env vars
    jsonparse/        Streaming JSON repair/parse
    oauth/            OAuth token management
    overflow/         Context overflow detection
    providers/        Provider-specific HTTP streaming
    ratelimit/        Rate limit header parsing
  auth/               Credential storage
  compaction/         Context window compaction
  config/             User/project configuration
  exec/               Bash command execution with sandbox/timeout
  extension/          Extension subprocess lifecycle (JSON-RPC)
    sdk/              Python SDK for extensions
  log/                Structured logging
  mcp/                MCP server connections
  models/             User-facing model config, aliases, provider selection
  modes/              Mode interface
    acp/              ACP JSON-RPC mode
    interactive/      Interactive TUI mode
      components/     Domain-specific TUI components
      theme/          Color schemes
    print/            Non-interactive print mode
  resources/          ResourceLoader interface, system prompts, project context
    clipboard/        OS clipboard image reading
  session/            AgentSession orchestration (prompt dispatch, plans, events)
    store/            Conversation history persistence
  tui/                Low-level terminal rendering
    components/       Reusable TUI widgets
  update/             Self-update
```

18 top-level packages (was 16: +`compaction`, +`exec`, -`core`).
