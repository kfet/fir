Use idiomatic Go. Keep it simple.

Split large files into smaller ones, making sure to update the mapping in `sync/UPSTREAM_MAP.md`

See `PLAN.md` for the current plan and process.

Do not ignore any issues, address them promptly, even if preexisting. Do not postpone any work, even if it seems daunting - just break it down into smaller tasks.

Do not leave incomplete or stubbed code. Ensure all code is functional and tested.

## Extensions

Extensions register via `init()` using `extension.Register(...)`. For an extension to be available at runtime, it **must** be blank-imported in `cmd/fir/app.go`:

```go
_ "github.com/kfet/fir/pkg/extensions/sandbox"
```

If you add a new extension package under `pkg/extensions/`, always add the corresponding blank import to `cmd/fir/app.go` — otherwise its `init()` never runs and the extension silently does not load.

## Changelog

When making user-visible changes, add an entry under `## [Unreleased]` in `CHANGELOG.md` using the appropriate subsection (`### Added`, `### Fixed`, `### Changed`, `### Removed`). Keep entries concise — one line per change. Do not bump `VERSION`; that happens during release.

**Order:** The changelog is kept in reverse-chronological order — the most recent version section is always at the top, directly below the `## [Unreleased]` section. Older versions appear further down. When adding entries within `## [Unreleased]`, prepend new items at the top of their subsection so the most recent change stays first.

## ACP Mode Port (Current Phase)

## Models

`pkg/ai/models_generated.go` is a generated file — **never edit it directly**. It is completely overwritten by `make generate-models`. Any manual edits will be lost.

To add or fix a model so it survives regeneration, add it to `applyOverridesAndAdditions()` in `cmd/generate-models/main.go`. Use a `!hasModel(all, provider, id)` guard so it doesn't create duplicates if the upstream API starts returning it.

The upstream analysis (`../pi-mono-acp/ACP-ANALYSIS.md`) concludes ACP cannot be an extension.
That analysis was written against pi-mono's **runtime-loaded JS extensions**. fir uses
**compiled-in Go extensions** which are more powerful in some ways (full Go access, can
import any package) but equally limited in the ways that matter for ACP:

1. **No transport control.** fir extensions (like TS ones) run *within* a mode. Neither can
   say "listen on stdin for ACP JSON-RPC." The `RunAcpMode()` entry point that sets up
   ndjson JSON-RPC 2.0 over stdio is outside extension scope in both systems.

2. **No multi-session management.** Both TS and Go extensions operate within a single
   `AgentSession`. ACP manages a `map[string]*PiSession` with per-session lifecycle,
   creating sessions on demand via `session/new`.

3. **No base tool replacement.** The upstream analysis says TS extensions "can *add* tools
   via `registerTool()` but cannot *replace* built-in tools." In fir, `RegisterTool` has a
   comment claiming "If name matches a built-in tool, it overrides it" — but this is **not
   actually implemented**: extension tools are stored in `runner.allTools` but never injected
   into `Agent.Tools`. Even if it were implemented, ACP needs to swap tool implementations
   at session creation time with client-delegating versions (file I/O via
   `connection.readTextFile()`/`writeTextFile()`, bash via `connection.createTerminal()`),
   which requires the `BaseToolsOverride` mechanism.

4. **No event serialization control.** Both TS and Go extensions receive events *after*
   emission. ACP's `handleEvent` intercepts raw `AgentSessionEvent`s and re-emits them
   in ACP's structured schema (with `title`, `kind`, `locations`, `content` including
   diff types). Neither extension system can transform the event wire format.

5. **No client capability negotiation.** ACP clients declare capabilities (`fs.readTextFile`,
   `fs.writeTextFile`, `terminal`) during `initialize`. No hook exists in either extension
   system for "the client supports X, so modify behavior Y."

**Bottom line:** The compiled-in nature of fir extensions gives them access to Go internals,
but the extension API surface is deliberately limited to the same operations as TS extensions.
The five architectural gaps above apply equally. ACP must be a standalone mode.

### Key constraints
- Uses `github.com/coder/acp-go-sdk` for stable ACP types + JSON-RPC transport + outbound helpers (saves ~1000 lines).
- The Go SDK (schema 0.10.7) is behind the TS SDK (0.14.1). Missing: `session/set_model`, `session/list`, `session/resume` and associated types. These are "unstable" ACP features that pi-mono-acp uses.
- **Workaround:** Use `acp.NewConnection` directly (not `AgentSideConnection`) to get the raw `MethodHandler` callback. This lets us handle ALL methods (stable + unstable) in one switch, while still using SDK types and outbound helpers. Define ~10 missing types in `types.go`.
- Core changes to existing Go code are minimal: session factory refactoring in `app.go`, `"acp"` mode in `args.go`, early-exit dispatch in `app.go`.

### Parallelization
- `types.go` has no deps — can start immediately (just ~10 unstable type structs)
- `helpers.go` depends only on `acp-go-sdk` + `types.go` — pure functions, highly testable
- `terminal.go` depends on `acp-go-sdk` + `types.go`
- `acp.go` depends on everything above + `pkg/core/*`
- The 3 core changes (`app.go` refactoring, `args.go`, `app.go` dispatch) are independent of the acp package files
