# Startup Performance

## Why interactive mode starts fast

In interactive (TUI) mode, fir defers **extension setup** to a background
goroutine so the prompt renders immediately. Extensions discover, spawn Python
processes, and complete JSON-RPC handshakes *after* `mode.Init()` / while
`mode.Run()` is already displaying the UI.

### Before (sequential)

```
run() → setupSession() → extension.Setup() [~1s] → mode.Init() → mode.Run()
                          ^^^^^^^^^^^^^^^^^^^^
                          blocks TUI for 600-1000ms
```

### After (deferred)

```
run() → setupSession(deferExtensions=true) → mode.Init() → mode.Run()
                                                   ↑
                                          go extension.Setup() [background]
```

### Why extensions were slow

`extension.Setup()` eagerly starts all extensions that don't declare an
`events:` filter in their frontmatter. Each eager extension spawns a Python
interpreter and performs a JSON-RPC `init` handshake. Even with concurrent
startup, the slowest handshake determines wall-clock time (~500-700ms for 5
eager extensions). Additionally, extensions subscribing to `session_start`
are lazy-started synchronously when the event fires, adding another ~200ms.

### Design constraints

- **Print mode** (`-p`, `--json`) still sets up extensions synchronously
  because the agent run begins immediately — there is no idle time to
  overlap with.
- **Extension tools** are registered asynchronously. This is safe because the
  user cannot submit a message faster than extensions finish loading (~1s).
- **`/reload` and `/reexec`** continue to work because `mode.SetExtensionSetup`
  is called from the goroutine after setup completes.

### Guarding against regressions

Unit tests in `cmd/fir/app_test.go` verify that `setupSession` with
`deferExtensions=true` returns `nil` for `extSetup` and a non-nil
`extensionOpts`. An e2e test in `tests/e2e/startup_test.go` asserts that
the TUI prompt appears within 3 seconds by launching `fir` with a slow mock
extension.
