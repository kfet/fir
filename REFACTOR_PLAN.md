# Refactor Plan: Reusable Go Module Reorganization

Goal: break the monolithic `pkg/ai` and grab-bag `pkg/core` into focused
sub-packages, rename `pkg/core` → `pkg/session` and move persistence to
`pkg/session/store`, extract shared mode bootstrap into `pkg/app`, and
fix the `tui` layering issue — all without adding top-level clutter to
`pkg/`.

## Principles

- Sub-packages over new top-level packages. Keep `pkg/` scannable.
- Every package has one clear responsibility.
- Lower packages (types, agent) stay slim so they're reusable.
- Modes depend on `pkg/app` for shared bootstrap, not duplicate it.
- `pkg/app` assembles things, it doesn't own them.

---

## Phase 1 — Split `pkg/ai` into sub-packages

Status: **pending**

`pkg/ai` is 12k LOC imported by everything. Split it so consumers can
depend on only what they need.

| New package | Contents | Current files |
|---|---|---|
| `pkg/ai` | Core types: `Message`, `Tool`, `ToolCall`, `Content`, `Usage`, `StopReason`, `Provider`, `Context`, `StreamOptions` | `types.go` |
| `pkg/ai/stream` | `AssistantMessageEventStream`, event types, `SimpleStreamFunction`, `StreamFunction` | `eventstream.go`, `stream.go` |
| `pkg/ai/models` | `Model`, `ModelCost`, registry (`RegisterModel`, `GetModel`, `GetProviders`, `GetModels`), generated models | `models.go`, `models_generated.go`, `registry.go` |
| `pkg/ai/jsonparse` | `ParseStreamingJSON`, `repairPartialJSON` | `jsonparse.go` |
| `pkg/ai/ratelimit` | `RateLimitInfo` | `ratelimit.go` |
| `pkg/ai/overflow` | `IsContextOverflow` | `overflow.go` |
| `pkg/ai/envkeys` | `GetEnvApiKey` and provider-specific key resolution | `envkeys.go` |

Steps:
1. Create sub-packages, move code, update imports project-wide.
2. `pkg/ai/providers` stays where it is (already a sub-package).
3. `pkg/ai/oauth` stays where it is.
4. `cmd/generate-models` targets `pkg/ai/models` instead of `pkg/ai`.
5. Run `make all` to confirm.

---

## Phase 2 — Rename `pkg/core` → `pkg/session`, break into sub-packages

Status: **pending**
Depends on: Phase 1

`pkg/core` mixes `AgentSession` orchestration with unrelated utilities,
and its name is vague. Rename to `pkg/session` (what it actually manages)
and extract utilities into sub-packages. Move current `pkg/session`
(persistence) into `pkg/session/store`.

| Package | Responsibility | Current location |
|---|---|---|
| `pkg/session` | `AgentSession` orchestration: prompt dispatch, plan tracking, event pub/sub, compaction | `pkg/core/agentsession.go`, `compaction_progress.go`, `timings.go`, `changelog.go`, `browser.go` |
| `pkg/session/store` | Conversation history persistence (read/write entries to disk) | `pkg/session/*.go` |
| `pkg/session/compaction` | Context window compaction | `pkg/core/compaction/` |
| `pkg/session/export` | HTML conversation export | `pkg/core/export.go` |
| `pkg/session/toolreg` | Default tool set assembly (`DefaultCodingTools`, `AllTools`, `ResolveServerTools`) | `pkg/core/sdk.go` |

Steps:
1. Rename `pkg/session` → `pkg/session/store`.
2. Rename `pkg/core` → `pkg/session`.
3. Move `pkg/core/compaction/` → `pkg/session/compaction/`.
4. Extract `export.go` → `pkg/session/export/`.
5. Extract `sdk.go` → `pkg/session/toolreg/`.
6. Move clipboard files to `pkg/resources/clipboard/` (Phase 4).
7. Move `bashexec.go` → `pkg/exec/` (bash execution isn't a session concern).
8. Update all imports project-wide.
9. Run `make all` to confirm.

---

## Phase 3 — Extract `pkg/app` (shared mode bootstrap)

Status: **pending**
Depends on: Phases 1–2

Each mode (`acp`, `interactive`, `print`) duplicates ~100 lines of
identical setup: auth, model registry, config, session manager,
extension manager, MCP, compaction. Extract into `pkg/app`.

```go
package app

type App struct {
    Auth       *auth.AuthStorage
    Models     *models.ModelRegistry
    Config     *config.Config
    Session    *session.SessionManager
    Extensions *extension.Manager
    MCP        *mcp.Manager
}

func New(ctx context.Context, opts Options) (*App, error)
```

Steps:
1. Identify the duplicated bootstrap in each mode's entry point.
2. Extract common setup into `pkg/app.New()`.
3. Refactor each mode to accept `*App` and add only mode-specific wiring.
4. Run `make all` to confirm.

---

## Phase 4 — Clean up `pkg/resources`

Status: **pending**
Can run in parallel with Phase 3.

Skills and extensions have different lifecycles. Split into sub-packages
and absorb clipboard from `pkg/core`.

| Package | Responsibility |
|---|---|
| `pkg/resources` | `ResourceLoader` interface, shared types |
| `pkg/resources/clipboard` | OS clipboard image reading (macOS/X11/Wayland) |
| `pkg/resources/skills` | Skill loading, skill frontmatter, embedded skills |
| `pkg/resources/extensions` | Extension discovery, extension frontmatter, embedded extensions |

Steps:
1. Move clipboard files from `pkg/core` into `pkg/resources/clipboard/`.
2. Move skill-specific code into `pkg/resources/skills/`.
3. Move extension-specific code into `pkg/resources/extensions/`.
4. Update imports in `pkg/extension`, `pkg/session`, modes.
5. Run `make all` to confirm.

---

## Phase 5 — Fix `tui` ↔ `modes/interactive` layering

Status: **pending**
Can run in parallel with Phases 3–4.

`pkg/tui` tests import `pkg/modes/interactive/components`, creating a
test-time upward dependency. Fix by moving shared display model types
down so `tui` tests don't reach into `modes/`.

Steps:
1. Identify which types from `modes/interactive/components` the tui
   tests actually use.
2. Move those types down into `pkg/tui/` or `pkg/tui/components/`.
3. Update imports, verify no circular deps.
4. Run `make all` to confirm.

---

## Proposed final `pkg/` layout

```
pkg/
  agent/              Runs the tool-use loop (prompt → LLM → tools → repeat)
    tools/            Built-in tool implementations
  ai/                 Core AI types (messages, tools, content blocks)
    envkeys/          Provider API key resolution from env vars
    jsonparse/        Streaming JSON repair/parse
    models/           Model registry, capabilities, costs
    oauth/            OAuth token management
    overflow/         Context overflow detection
    providers/        Provider-specific HTTP streaming
    ratelimit/        Rate limit header parsing
    stream/           Event stream for assistant responses
  app/                Wires auth+models+config+session+extensions into ready-to-use App
  auth/               Credential storage
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
    extensions/       Embedded extension loading
    skills/           Embedded skill loading
  session/            AgentSession orchestration (prompt dispatch, plans, events)
    compaction/       Context window compaction
    export/           HTML conversation export
    store/            Conversation history persistence
    toolreg/          Default tool set assembly
  tui/                Low-level terminal rendering
    components/       Reusable TUI widgets
  update/             Self-update
```

17 top-level packages (current: 16, +`app`, +`exec`, -`core`). All
other splits are sub-packages.
