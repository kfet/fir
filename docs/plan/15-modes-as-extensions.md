# Could Modes Be Extensions?

Analysis of the minimal changes to tau's extension system that would allow
ACP mode (and potentially RPC/print modes) to be implemented on top of it.

## What a mode actually is

Every mode in tau follows the same pattern:

1. Receive (or create) an `AgentSession`
2. Own a transport (stdin/stdout text, JSON lines, JSON-RPC, TUI)
3. Translate between the transport protocol and `session.Prompt()` / `session.Subscribe()`
4. Handle mode-specific concerns (slash commands, event formatting, etc.)

Current modes and what they need:

| Mode | Sessions | Transport | Needs from environment |
|------|----------|-----------|----------------------|
| Print | 1 pre-created | stdout text or JSON | session |
| RPC | 1 pre-created | stdin/stdout JSON lines | session |
| Interactive | 1 pre-created | TUI terminal | session, keybindings, theme, extension UI |
| ACP | N on-demand | stdin/stdout ndjson JSON-RPC 2.0 | session factory, client capabilities |

## The five gaps (revisited for Go)

The upstream analysis identified 5 reasons ACP can't be an extension. Here's how
each maps to the actual Go code, and what it would take to close the gap:

### Gap 1: No transport control

Extensions run within a mode. They can't say "I'll handle stdin/stdout now."

**To close:** Add `RegisterMode(name, handler)` to the extension API. The handler
gets a `ModeContext` with raw I/O access and runs the main loop.

### Gap 2: No multi-session management

Extensions bind to one `AgentSession` (via `extension.Setup(session, ...)`).
ACP needs to create sessions on demand with per-session cwd and tools.

**To close:** Give the mode handler a session factory instead of a pre-created session.

### Gap 3: No base tool replacement

Extension `RegisterTool` stores tools but never injects them into `Agent.Tools`.
Even if it did, ACP needs tools swapped at session creation time with client-delegating
versions (file I/O via ACP client, bash via ACP terminals).

**To close:** The session factory (gap 2) naturally handles this — modes pass custom
tools via `CreateAgentSessionOptions.Tools`. No extension API change needed.

### Gap 4: No event serialization control

Extensions receive events post-emission. ACP needs to intercept raw events and
re-emit them in ACP's schema (structured tool calls with title/kind/locations/diffs).

**To close:** Modes own the session, so they call `session.Subscribe()` themselves.
No extension API change needed — this is just "don't subscribe on behalf of the mode."

### Gap 5: No client capability negotiation

No extension hook for "the client supports X, modify behavior Y."

**To close:** Modes handle this in their transport layer (ACP's `initialize` handler).
No extension API change needed.

## The minimal change

Gaps 3, 4, and 5 are all solved by gaps 1 and 2. So the minimal change is:

### 1. Add `RegisterMode` to the extension API (~15 lines)

```go
// In extension/types.go:

// ModeHandler runs a mode's main loop. It blocks until the mode exits.
type ModeHandler func(ctx ModeContext) error

// ModeContext provides everything a mode needs.
type ModeContext struct {
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer

    // NewSession creates an AgentSession. Can be called multiple times.
    // Applies CLI-resolved defaults (auth, model, settings, resources).
    // Options override specific fields (cwd, tools) for the new session.
    NewSession func(opts ...SessionOption) (*core.CreateAgentSessionResult, error)

    // Resolved environment (read-only)
    Cwd             string
    AgentDir        string
    SettingsManager *core.SettingsManager
    AuthStorage     *core.AuthStorage
    ModelRegistry   *core.ModelRegistry
    ResourceLoader  core.ResourceLoader
}

type SessionOption func(*core.CreateAgentSessionOptions)

func WithCwd(cwd string) SessionOption {
    return func(o *core.CreateAgentSessionOptions) { o.Cwd = cwd }
}

func WithTools(tools []agent.AgentTool) SessionOption {
    return func(o *core.CreateAgentSessionOptions) { o.Tools = tools }
}
```

Then add to `API`:
```go
type API interface {
    // ... existing methods ...
    RegisterMode(name string, handler ModeHandler)
}
```

### 2. Add mode storage + lookup to Runner (~10 lines)

```go
// In extension/runner.go:
type Runner struct {
    // ... existing fields ...
    allModes map[string]ModeHandler
}

func (r *Runner) GetMode(name string) ModeHandler {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.allModes[name]
}
```

### 3. Extract session factory from `app.go` (~40 lines refactored)

The current `setupSession()` creates one session eagerly. Refactor it into:
- `resolveEnvironment(args)` → returns resolved config (auth, models, settings, resources)
- A closure `func newSession(opts ...SessionOption) (*Result, error)` that calls `core.CreateAgentSession`

This is just splitting existing code — no new logic.

### 4. Mode dispatch in `app.go` (~5 lines)

```go
// Before the current mode switch:
if handler := extRunner.GetMode(string(args.OutputMode)); handler != nil {
    return handler(modeCtx)
}
```

### Total: ~70 lines of new code + ~40 lines of refactoring

## What each mode looks like as an extension

### Print mode

```go
func init() {
    extension.Register("print-mode", func(api extension.API) {
        api.RegisterMode("text", func(ctx extension.ModeContext) error {
            result, err := ctx.NewSession()
            if err != nil { return err }
            defer result.Session.Close()
            return printmode.Run(result.Session, printmode.Options{...})
        })
    })
}
```

### RPC mode

```go
func init() {
    extension.Register("rpc-mode", func(api extension.API) {
        api.RegisterMode("rpc", func(ctx extension.ModeContext) error {
            result, err := ctx.NewSession()
            if err != nil { return err }
            defer result.Session.Close()
            server := rpc.NewServerWithIO(result.Session, ctx.Stdin, ctx.Stdout)
            return server.Run()
        })
    })
}
```

### ACP mode

```go
func init() {
    extension.Register("acp-mode", func(api extension.API) {
        api.RegisterMode("acp", func(ctx extension.ModeContext) error {
            agent := NewPiAgent(ctx) // ctx has NewSession, Stdin, Stdout
            return RunJsonRpc(agent, ctx.Stdin, ctx.Stdout) // blocks forever
        })
    })
}
```

ACP calls `ctx.NewSession(WithCwd(params.Cwd), WithTools(acpTools))` inside
its `session/new` handler — creating sessions on demand with custom tools.

### Interactive mode

```go
func init() {
    extension.Register("interactive-mode", func(api extension.API) {
        api.RegisterMode("interactive", func(ctx extension.ModeContext) error {
            result, err := ctx.NewSession()
            if err != nil { return err }
            defer result.Session.Close()
            mode := interactive.NewInteractiveMode(result.Session, ...)
            return mode.Run()
        })
    })
}
```

Interactive mode works but needs keybindings and themes from `ModeContext`.
These could be added as needed — the context can grow without breaking
existing modes since modes only use what they need.

## Should we do this?

### Arguments for

- **Uniform registration.** All modes use the same `init()` + `Register()` pattern.
  Adding a new mode = adding a package with `init()`. No `app.go` switch to update.
- **Modes can also register tools, commands, flags.** An ACP mode extension could
  register `--acp-timeout` as a flag through the same API it uses to register the mode.
- **Clean separation.** `app.go` resolves config and dispatches. Modes own their lifecycle.
  Currently `app.go` has interleaved concerns (session creation, mode dispatch, extension
  setup, file processing).
- **The session factory is useful anyway.** ACP needs it regardless. Extracting it from
  `setupSession()` improves the code even without the mode registry.

### Arguments against

- **It's a registry for 4 things.** tau is a compiled binary. A switch statement over
  4 known modes is simpler than a registry + factory pattern. The registry only pays off
  if third parties add modes, which requires a plugin system tau doesn't have.
- **Interactive mode is awkward.** It needs keybindings, themes, extension UI wiring —
  things no other mode needs. Either `ModeContext` grows to accommodate it, or
  interactive mode reaches outside the context. Neither is clean.
- **The extension lifecycle is per-session.** The extension system's `Setup()`,
  `EmitSessionStart()`, tool hooks, event bridge — all bind to one session. A mode
  like ACP that manages multiple sessions needs to call `extension.Setup()` per session
  independently. This works but the mode is doing all the wiring that `app.go` currently
  does — the factory only saves the `CreateAgentSession` call.

### Recommendation

**Do the session factory refactoring now (it's needed for ACP anyway), but defer
the mode registry.** The concrete steps:

1. Extract `resolveEnvironment()` from `setupSession()` in `app.go`
2. Create a `SessionFactory` that ACP can call repeatedly
3. Keep the mode dispatch as a simple switch in `app.go` (add `case "acp":`)
4. If a fifth or sixth mode appears, reconsider the registry

This gives ACP what it needs (session factory with tool overrides) without
adding registry machinery for a 4-entry switch. The refactoring makes the
eventual registry trivial to add later if warranted.
