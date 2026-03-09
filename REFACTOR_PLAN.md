# pkg/core Refactoring Plan

## Problem

`pkg/core` is a god package (~10K lines, 30+ files) with at least 8 distinct responsibilities. This makes it impossible to use `pkg/agent` or `pkg/ai` as standalone libraries without pulling in UI code, credential storage, settings, and the entire resource loading system.

## Goals

1. Clear responsibility boundaries — each package does one thing
2. `pkg/agent` + `pkg/ai` usable as a standalone public module (no UI, no config, no resource loading)
3. Consumers can opt in to only what they need (e.g., skip the plan tool, skip clipboard support)
4. No import cycles, no god packages

## Current `pkg/core` Responsibilities

| Responsibility | Files | Lines |
|---|---|---|
| Agent session orchestration | `agentsession.go` | 1539 |
| Session persistence | `session.go`, `session_sidecar.go` | 1400 |
| Settings & config | `settings.go`, `configvalue.go`, `defaults.go` | 1200 |
| Model registry & resolution | `modelregistry.go`, `modelresolver.go` | 1100 |
| Auth/credential storage | `authstorage.go`, `authstorage_flock*.go` | 500 |
| Resource loading | `resourceloader.go`, `skills.go`, `builtin_skills.go`, `builtin_extensions.go`, `frontmatter.go`, `prompttemplates.go` | 2200 |
| UI concerns | `keybindings.go`, `clipboard*.go`, `messages.go`, `footerdataprovider.go`, `export.go` | 1000 |
| Infra utilities | `eventbus.go`, `timings.go`, `bashexec.go`, `browser.go` | 400 |

## Target Package Layout

```
pkg/
├── agent/          # Agent loop + tool types (already clean)
│   └── tools/      # Default tool implementations (moved from core/tools/)
├── ai/             # LLM providers + model types (already clean)
├── auth/           # Credential storage (NEW — from authstorage*.go)
├── config/         # Settings manager, config values, defaults (NEW)
├── eventbus/       # Generic pub/sub (NEW)
├── extension/      # Extension discovery & JSON-RPC (unchanged)
├── log/            # Logging (unchanged)
├── mcp/            # MCP client (unchanged)
├── models/         # Model registry + resolver (NEW)
├── platform/       # OS utilities: clipboard, browser (NEW)
├── resources/      # .fir/ loading, skills, extensions, prompts (NEW)
├── session/        # Session persistence: save/load/list/tree (NEW)
├── core/           # Thin orchestration: AgentSession + DI wiring (SLIMMED)
├── modes/          # Interactive, print, ACP (unchanged structure)
│   └── interactive/
│       └── ...     # keybindings, messages, footer, export move here
├── tui/            # Terminal UI (unchanged)
├── update/         # Self-update (unchanged)
└── usage/          # Usage tracking (unchanged)
```

## Dependency Graph (after refactor)

```
log                          (leaf)
 ↑
ai                           (leaf — depends only on log)
 ↑
agent, agent/tools           (depends on ai, log)
 ↑
auth, eventbus, platform     (leaves — stdlib only)
 ↑
config                       (depends on ai)
 ↑
models                       (depends on ai, config, auth)
 ↑
session                      (depends on ai)
 ↑
resources                    (depends on ai, config)
 ↑
core (slim)                  (depends on agent, ai, models, session, resources, config — via interfaces)
 ↑
extension, mcp               (depends on agent, ai, core interfaces)
 ↑
modes/*                      (depends on core, tui, config, models, session, resources)
```

---

## Phase 1: Extract Leaf Packages

Low risk. No complex dependency chains. Each extraction is independent.

### 1a. `pkg/auth` ← `authstorage*.go`

- **Move:** `authstorage.go`, `authstorage_flock.go`, `authstorage_flock_windows.go`
- **Types:** `AuthStorage`, `AuthCredential`, `CredentialType`, `AuthStorageBackend`, `FileAuthStorageBackend`, `InMemoryAuthStorageBackend`
- **Internal deps:** none (stdlib + `golang.org/x/term`)
- **Consumers to update:** `cmd/fir/login.go`, `modes/acp/auth.go`, `core/modelregistry.go`

### 1b. `pkg/config` ← `settings.go`, `configvalue.go`, `defaults.go`

- **Types:** `SettingsManager`, `ConfigValue`, default model/dir constants
- **Internal deps:** `pkg/ai` (for default model IDs only)
- **Consumers to update:** many — `cmd/fir/app.go`, all modes, `modelregistry.go`
- **Note:** Largest consumer surface; do this carefully

### 1c. `pkg/eventbus` ← `eventbus.go`

- **Types:** `EventBus`, `EventBusController`, `EventHandler`
- **Internal deps:** none (stdlib only)
- **Consumers to update:** `agentsession.go`, modes

---

## Phase 2: Extract Domain Packages

### 2a. `pkg/models` ← `modelregistry.go`, `modelresolver.go`

- **Types:** `ModelRegistry`, `ModelDefinition`, `ProviderConfig`, `ModelOverride`, scope resolution functions
- **Internal deps:** `pkg/ai`, `pkg/config`, `pkg/auth`
- **Consumers to update:** `cmd/fir/app.go`, modes, `agentsession.go`

### 2b. `pkg/session` ← `session.go`, `session_sidecar.go`

- **Types:** `SessionManager`, `SessionEntry`, `SessionHeader`, `SessionTreeNode`, `SessionListInfo`, `SessionContext`
- **Internal deps:** `pkg/ai` (message types in entries)
- **Consumers to update:** `cmd/fir/app.go`, modes, `agentsession.go`

### 2c. `pkg/resources` ← `resourceloader.go`, `skills.go`, `builtin_skills.go`, `builtin_extensions.go`, `frontmatter.go`, `prompttemplates.go`

- **Types:** `ResourceLoader`, `Skill`, `BuiltinExtension`, `ParsedFrontmatter`, `PromptTemplate`, `PathMetadata`
- **Internal deps:** `pkg/ai`, `pkg/config`
- **Consumers to update:** `cmd/fir/app.go`, modes, `agentsession.go`, `extension/`

---

## Phase 3: Move UI Concerns Out of Core

### 3a. `keybindings.go` → `pkg/modes/interactive/`

- Only file in core that imports `pkg/tui` — removing this eliminates the core→tui dependency entirely
- Types: `KeybindingsManager`, `AppAction`

### 3b. `clipboard*.go`, `browser.go` → `pkg/platform/`

- OS-level utilities (xdg-open, pbcopy, xclip, etc.)
- Types: `ClipboardImage`, clipboard/browser functions
- No internal deps

### 3c. `messages.go`, `footerdataprovider.go`, `export.go` → `pkg/modes/interactive/`

- Presentation-layer types only used by interactive mode
- Types: `BashExecutionMessage`, `CustomMessage`, `BranchSummaryMessage`, `CompactionSummaryMessage`, `TextContent`
- `FooterDataProvider` is TUI-specific

---

## Phase 4: Dependency Injection on AgentSession

### 4a. Define Interfaces

Define in `pkg/agent` (or keep in slimmed `pkg/core`):

```go
// ToolProvider supplies the set of tools available to the agent.
type ToolProvider interface {
    Tools() []AgentTool
}

// SessionStore handles persistence of conversation history.
type SessionStore interface {
    Save(entries []SessionEntry) error
    Load() ([]SessionEntry, error)
}

// ModelResolver maps a scope/alias to a concrete model + provider.
type ModelResolver interface {
    Resolve(scope string) (ai.Model, ai.Provider, error)
}

// CompactionNotifier reports compaction progress (optional).
type CompactionNotifier interface {
    OnCompactionProgress(phase, delta string)
}
```

### 4b. Refactor `AgentSessionOptions`

Replace concrete dependencies with interfaces:

```go
type AgentSessionOptions struct {
    // Required
    ModelResolver  ModelResolver
    ToolProvider   ToolProvider

    // Optional (nil = no-op defaults)
    SessionStore        SessionStore
    CompactionNotifier  CompactionNotifier
    EventBus            EventBus
    UsageTracker        UsageTracker
}
```

### 4c. Move `core/tools/` → `pkg/agent/tools/`

- Plan, bash, read, write, edit, web_search, etc. become opt-in defaults
- A public consumer constructs their own `ToolProvider` with whichever subset they want

---

## Phase 5: Wire Up & Validate

### 5a. Update All Consumers

- `cmd/fir/app.go` — construct and inject `auth.AuthStorage`, `config.SettingsManager`, `models.ModelRegistry`, etc.
- `pkg/modes/*` — update imports from `core.X` to new package paths
- `pkg/extension/` — update imports

### 5b. Verify No Import Cycles

```bash
go vet ./...
```

### 5c. Full Build & Test

```bash
make all
```

### 5d. Verify Standalone Usage

Confirm that this compiles without pulling in UI/config/resources:

```go
import (
    "github.com/kfet/fir/pkg/agent"
    "github.com/kfet/fir/pkg/ai"
)
```

---

## Phase 6: Optional — Go Sub-modules

For independent versioning and consumption:

- Add `pkg/ai/go.mod` → `github.com/kfet/fir/pkg/ai`
- Add `pkg/agent/go.mod` → `github.com/kfet/fir/pkg/agent`
- Use `go.work` at repo root for local development
- Enables `go get github.com/kfet/fir/pkg/ai@v1.2.3`

---

## Progress Tracker

Last reviewed: **2026-03-08**

### Phase 1: Extract Leaf Packages — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 1a. `pkg/auth` | ✅ Done | `authstorage.go`, flock files moved with tests |
| 1b. `pkg/config` | ✅ Done | `settings.go`, `configvalue.go`, `defaults.go` moved with tests |
| 1c. `pkg/eventbus` | ⏭️ Skipped | EventBus was removed from the codebase entirely; no extraction needed |

### Phase 2: Extract Domain Packages — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 2a. `pkg/models` | ✅ Done | `modelregistry.go`, `modelresolver.go` moved with tests |
| 2b. `pkg/session` | ✅ Done | `session.go`, `session_sidecar.go` moved with tests |
| 2c. `pkg/resources` | ✅ Done | All resource files moved; also absorbed `slashcmds.go` and `systemprompt.go` from core |

### Phase 3: Move UI Concerns Out of Core — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 3a. `keybindings.go` → interactive | ✅ Done | Moved to `pkg/tui/` |
| 3b. `clipboard*.go`, `browser.go` → `pkg/platform/` | ✅ Done | `clipboard.go`, `clipboardimage.go`, `browser.go` in `pkg/platform/` |
| 3c. `messages.go` → `pkg/msg/` | ✅ Done | Moved to `pkg/msg/` |
| 3d. `export.go` | ✅ Stays | Stays in `pkg/core/` — `ExportToHTML` is an `AgentSession` method called by 3 consumers (cmd/fir, interactive, acp) |
| 3e. `footerdataprovider.go` → interactive | ✅ Done | Moved to `pkg/modes/interactive/` with tests |
| 3f. `bashexec.go` → platform | ✅ Done | Moved to `pkg/platform/bashexec.go` with tests; core imports `platform.ExecuteBash` |
| 3g. Update UPSTREAM_MAP.md paths | ✅ Done | Fixed stale `pkg/core/` → new package paths |

### Phase 4: Dependency Injection on AgentSession — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 4a. Define interfaces | ❌ Removed | Dead code — no consumers used them; deleted `interfaces.go` |
| 4b. Refactor `AgentSessionOptions` | ⏭️ Skipped | All consumers use concrete types directly (20+ methods); no alternate implementations exist |
| 4c. Move `core/tools/` → `pkg/agent/tools/` | ✅ Done | 26 files moved, 9 consumers updated |

### Phase 5: Wire Up & Validate — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 5a. Update all consumers | ✅ Done | All imports updated during Phase 1–4 extractions; no stale `core.X` refs for moved types |
| 5b. Verify no import cycles | ✅ Done | `go vet ./...` clean |
| 5c. Full build & test | ✅ Done | `make all` passes |
| 5d. Verify standalone usage | ✅ Done | `pkg/ai` has zero internal deps; `pkg/agent` depends only on `ai` + `log` |

### Phase 6: Consolidate Small Packages — ✅ COMPLETE

| Step | Status | Notes |
|------|--------|-------|
| 6a. `pkg/msg` → `pkg/session` | ✅ Done | `messages.go`, `messages_test.go` merged; all consumers updated |
| 6b. `pkg/debug` → `pkg/log` | ✅ Done | `debug.go`, `debug_test.go` merged; 2 consumers updated |
| 6c. `pkg/platform` → `pkg/core` | ✅ Done | All 8 platform files merged into core; 4 consumers updated |
| 6d. `pkg/usage` → `cmd/fir` | ✅ Done | 3 files moved to cmd/fir; `app.go` updated |

The extraction phases created 18 top-level packages under `pkg/`. Several are too small to justify their own directory. This phase merges them back into natural homes to reach **14 top-level packages**.

#### 6a. `pkg/msg` → `pkg/session` ✅ Done

#### 6b. `pkg/debug` → `pkg/log` ✅ Done

#### 6c. `pkg/platform` → `pkg/core` ✅ Done

#### 6d. `pkg/usage` → `cmd/fir` internal ✅ Done

#### Result

After Phase 6, the top-level layout is:

```
pkg/                        # 14 packages (down from 18)
├── agent/                  # Agent loop + tools/
├── ai/                     # LLM types + providers/ + oauth/
├── auth/                   # Credential storage
├── config/                 # Settings + defaults
├── core/                   # Orchestration + platform utils + compaction/
├── extension/              # Extension discovery & JSON-RPC
├── log/                    # Logging + debug
├── mcp/                    # MCP client
├── models/                 # Model registry + resolver
├── modes/                  # interactive/ + print/ + acp/
├── resources/              # .fir/ loading, skills, prompts
├── session/                # Session persistence + message types
├── tui/                    # Terminal UI components
└── update/                 # Self-update
```

### Phase 7: Split God Files — ✅ COMPLETE

No logic changes — just splitting large files into multiple files within the same package.

#### 7a. `pkg/modes/interactive/mode.go` (3,308 lines, 94 functions) ✅ Done

Split into:
- `commands.go` — slash command dispatch + all `handle*` functions (~1,483 lines)
- `events.go` — agent subscription, chat management, footer data (~415 lines)
- `selectors.go` — all `show*` overlays, OAuth, tree/fork selectors (~745 lines)
- `mode.go` — Init, Run, Shutdown, setupEditorHandlers, setupAutocomplete, lifecycle (~713 lines)

#### 7b. `pkg/modes/acp/acp.go` (1,690 lines, 39 functions) ✅ Done

Split into:
- `methods.go` — all RPC method handlers (~1,089 lines)
- `tools.go` — all ACP tool builder functions (~262 lines)
- `acp.go` — core structs, RunAcpMode, helpers (~376 lines)

### Phase 8: Optional — Go Sub-modules — ❌ NOT STARTED (low priority)

For independent versioning and consumption:

- Add `pkg/ai/go.mod` → `github.com/kfet/fir/pkg/ai`
- Add `pkg/agent/go.mod` → `github.com/kfet/fir/pkg/agent`
- Use `go.work` at repo root for local development

### Current `pkg/core` State

**6 source files, ~2,365 lines** (down from ~10,400 — **77% reduction**)

| File | Lines | Status |
|------|-------|--------|
| `agentsession.go` | 1621 | Stays (core orchestration) |
| `sdk.go` | 385 | Stays (convenience constructor) |
| `changelog.go` | 138 | Stays |
| `export.go` | 126 | Stays (AgentSession method, 3 callers) |
| `timings.go` | 71 | Stays |
| `compaction_progress.go` | 24 | Stays |

⚠️ **Build status:** `make all` passes ✅ (verified 2026-03-08, Phase 7 complete)
