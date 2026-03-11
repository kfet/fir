# Refactor Plan: Reusable Go Module Reorganization

Goal: break the monolithic `pkg/ai` and grab-bag `pkg/core` into focused
sub-packages, extract shared mode bootstrap into `pkg/app`, and fix the
`tui` layering issue — all without adding top-level clutter to `pkg/`.

## Principles

- Sub-packages over new top-level packages. Keep `pkg/` scannable.
- Every package has one clear responsibility.
- Lower packages (types, agent, session) stay slim so they're reusable.
- Modes depend on `pkg/app` for shared bootstrap, not duplicate it.

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

## Phase 2 — Break up `pkg/core` into sub-packages

Status: **pending**  
Depends on: Phase 1

`pkg/core` mixes `AgentSession` orchestration with unrelated utilities.
Keep utilities as sub-packages of `core/`.

| Package | Contents | Current files |
|---|---|---|
| `pkg/core` | `AgentSession`, plan management, `CompactionRunner` interface, timings, changelog | `agentsession.go`, `compaction_progress.go`, `timings.go`, `changelog.go` |
| `pkg/core/exec` | `BashExecutor` | `bashexec.go` |
| `pkg/core/clipboard` | Clipboard text + image read (X11/Wayland/macOS) | `clipboard.go`, `clipboardimage.go` |
| `pkg/core/export` | `ExportToHTML`, `WriteConversationHTML` | `export.go` |
| `pkg/core/toolreg` | `DefaultCodingTools`, `AllTools`, `ResolveServerTools` | `sdk.go` |

Steps:
1. Create sub-packages, move code, update imports.
2. `core/compaction/` stays as-is.
3. `core/browser.go` stays in `core` (tiny, used by AgentSession).
4. Run `make all` to confirm.

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

Skills and extensions have different lifecycles. Split if it reduces
coupling; otherwise leave as-is with clearer file naming.

| Package | Contents |
|---|---|
| `pkg/resources` | `ResourceLoader` interface, shared types |
| `pkg/resources/skills` | Skill loading, skill frontmatter, embedded skills |
| `pkg/resources/extensions` | Extension discovery, extension frontmatter, embedded extensions |

Steps:
1. Move skill-specific code into `pkg/resources/skills`.
2. Move extension-specific code into `pkg/resources/extensions`.
3. Update imports in `pkg/extension`, `pkg/core`, modes.
4. Run `make all` to confirm.

---

## Phase 5 — Fix `tui` ↔ `modes/interactive` layering

Status: **pending**  
Can run in parallel with Phases 3–4.

`pkg/tui` tests import `pkg/modes/interactive/components`, creating a
test-time upward dependency. Fix by moving shared display model types
into `pkg/tui/types` or `pkg/tui/components` so `tui` tests don't
reach into `modes/`.

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
  agent/              Agent loop, plan, toolset, types
    tools/            Built-in tool implementations
  ai/                 Core AI types (Message, Tool, Content, etc.)
    envkeys/          Provider API key resolution
    jsonparse/        Streaming JSON repair/parse
    models/           Model registry + generated model data
    oauth/            OAuth token management
    overflow/         Context overflow detection
    providers/        Provider-specific streaming (Anthropic, OpenAI, etc.)
    ratelimit/        Rate limit info
    stream/           AssistantMessageEventStream, event types
  app/                Shared bootstrap (auth + models + config + session + extensions)
  auth/               Credential storage
  config/             User/project configuration
  core/               AgentSession orchestration
    clipboard/        Clipboard access (X11/Wayland/macOS)
    compaction/       Context compaction
    exec/             Bash command execution
    export/           HTML conversation export
    toolreg/          Default tool registration
  extension/          Extension lifecycle management
    sdk/              Extension SDK (Python)
  log/                Structured logging
  mcp/                MCP client management
  models/             ModelRegistry (user model config)
  modes/              Mode interface
    acp/              ACP mode
    interactive/      Interactive TUI mode
      components/     TUI components
      theme/          Theme definitions
    print/            Non-interactive print mode
  resources/          Resource loading
    extensions/       Embedded extension loading
    skills/           Embedded skill loading
  session/            Session persistence
  tui/                Terminal rendering primitives
    components/       Reusable TUI widgets
  update/             Self-update
```

Top-level `pkg/` count: 16 → 17 (+`app/`). All other splits are sub-packages.
