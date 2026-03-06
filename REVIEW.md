# External Process Extensions — Code Review

Reviewer: Claude (automated review)  
Date: 2026-03-01  
Scope: `pkg/extproc/`, `pkg/extproc/sdk/`

---

## CRITICAL

### C1. Build is broken — `pkg/extension/integration.go` references undefined `extMgr` and `extproc`

**File:** `pkg/extension/integration.go:133,161,239,254+`

`integration.go` references `extMgr` (a `*extproc.Manager`) that is never declared in that function scope, and `extproc` is never imported. The package doesn't compile:

```
pkg/extension/integration.go:161:39: undefined: extMgr
pkg/extension/integration.go:239:78: undefined: extproc
```

`go build ./pkg/extproc/` also fails because it transitively depends on `pkg/extension`. The entire build is broken.

**Fix:** Complete the wiring in `integration.go` — add the `extproc` import and pass `extMgr` as a parameter to the relevant functions, or declare it.

---

### C2. Codec has no concurrency protection — concurrent writes corrupt the stream

**File:** `pkg/extproc/jsonrpc.go`

`Codec.encoder` (`json.Encoder`) is not thread-safe. `Bridge.EmitEvent`, `Bridge.CallHook`, and `Bridge.handleInbound` all call `codec.Write*` methods concurrently (from the Run goroutine, from `EmitEvent` callers, and from `RegisterTools` execute callbacks). Two concurrent writes will interleave JSON on stdout, producing corrupt messages.

**Fix:** Add a `sync.Mutex` to `Codec` and lock it in every `Write*` method. The Python SDK already does this (`_write_lock`).

---

### C3. Goroutine leak in `Handshake` on timeout

**File:** `pkg/extproc/capability.go:44-52`

When the 5s timeout fires, `Handshake` returns an error but the goroutine doing `codec.ReadMessage()` is never cancelled — it blocks forever on the pipe read. Since the caller (`manager.go:81`) then calls `proc.Stop()`, the pipe eventually closes, but if `Stop` itself times out or the process doesn't die cleanly, this goroutine leaks.

**Fix:** After timeout, close the codec's reader (or kill the process) to unblock the goroutine. Or restructure to use the process's `waitDone` channel as a secondary signal.

---

### C4. Goroutine leak in `Process` — stderr scanner goroutine never terminates on `Restart`

**File:** `pkg/extproc/process.go:80-84`

On `Restart()`, `startLocked()` spawns a new stderr scanner goroutine, but the old one from the previous `Start()` may still be running (blocked on `scanner.Scan()` if stderr hasn't been closed yet). Over multiple restarts, this accumulates leaked goroutines.

Similarly, the `Wait` goroutine (line 87-90) from the old process is orphaned — `waitDone` is reassigned to a new channel without draining the old one.

**Fix:** Ensure `Stop()` is fully complete (process dead, pipes closed) before `startLocked()` creates new goroutines. The current `Restart` does call `Stop`, but `startLocked` doesn't verify the old goroutines have exited.

---

### C5. `Process.Stop` sends `os.Interrupt` (SIGINT) instead of `SIGTERM`

**File:** `pkg/extproc/process.go:109`

The doc says "sends SIGTERM" but the code sends `os.Interrupt` which is `SIGINT` on Unix. The design doc (§5) specifies SIGTERM. SIGINT may not be handled the same way by all extension processes (e.g., Python scripts may raise `KeyboardInterrupt`).

**Fix:** Use `syscall.SIGTERM` instead of `os.Interrupt`.

---

## IMPORTANT

### I1. `Bridge.Run` reader goroutine leak when context is cancelled

**File:** `pkg/extproc/bridge.go:50-62`

When `ctx` is cancelled, `Run` returns `ctx.Err()`, but the inner goroutine calling `codec.ReadMessage()` blocks forever on the pipe read. It only exits when the pipe is closed (by `proc.Stop()`). The caller (manager) does call `proc.Stop()` after `cancel()`, but there's a window where the goroutine is dangling.

**Fix:** This is mitigated by the manager's `Stop()` flow, but the goroutine should be tracked. Consider closing the codec reader when context is done.

---

### I2. `handleInbound` silently swallows JSON unmarshal errors for params

**File:** `pkg/extproc/bridge.go:71,83,94,105,...`

Every inbound request handler does `_ = json.Unmarshal(*req.Params, &p)` — ignoring parse errors. If an extension sends malformed params, the handler proceeds with zero-valued fields and returns `{"ok": true}`, silently succeeding on garbage input.

**Fix:** Check unmarshal errors and return a JSON-RPC error (`-32602 Invalid params`).

---

### I3. `notify` and `set_status` inbound methods are no-ops

**File:** `pkg/extproc/bridge.go:75-80,154-161`

Both methods return `{"ok": true}` without doing anything. The doc (§2.4) lists these as real capabilities. Extension authors will call them expecting behavior and get silent success with no effect.

**Fix:** Either implement them (wire to the event bus or UI context) or return an error indicating they're unimplemented.

---

### I4. `exec` inbound handler doesn't pass `Args` correctly

**File:** `pkg/extproc/bridge.go:82-92`

The handler parses `command` and `args` fields, but the Python SDK's `Context.exec()` sends `{"command": command, "timeout": timeout_sec}` — no `args` field. The Go side's `api.Exec(p.Command, p.Args)` receives an empty args slice. There's a mismatch between the Python SDK and Go handler on the `exec` method signature.

**Fix:** Align the Python SDK and Go handler. Decide whether `exec` takes a single shell command string or a command + args array.

---

### I5. `SDKEnv` overwrites rather than prepends to existing env vars

**File:** `pkg/extproc/sdk/env.go` + `pkg/extproc/process.go:65`

`SDKEnv` returns `PYTHONPATH=/cache/path/python`. `process.go` does `cmd.Env = append(os.Environ(), env...)`. Since env vars are last-wins in Go's `exec.Cmd`, this **overwrites** any existing `PYTHONPATH` the user had set, rather than prepending to it. This could break extensions that depend on other Python packages.

**Fix:** In `SDKEnv`, read the current env var and prepend: `"PYTHONPATH=" + sdkPath + ":" + os.Getenv("PYTHONPATH")`.

---

### I6. Trust store TOCTOU — hash check races with file modification

**File:** `pkg/extproc/trust.go` + `pkg/extproc/manager.go:71-79`

`ComputeHash` reads the file, then `proc.Start()` executes it. An attacker who controls `.fir/extensions/` could swap the file between hash check and execution. This is inherent to file-based trust but worth noting.

**Fix:** Consider opening the file, hashing it, then executing from the same fd (via `/proc/self/fd/N` on Linux) or at minimum document this as a known limitation.

---

### I7. `CallHook` in `Manager` is sequential across all bridges

**File:** `pkg/extproc/manager.go:124-140`

Hooks are called sequentially on each bridge. If one extension is slow (up to the timeout), all subsequent extensions are delayed. With N extensions and a 5s timeout, worst case is N*5s.

**Fix:** Fan out hook calls concurrently with `sync.WaitGroup` or `errgroup`.

---

### I8. Python SDK: response routing in `_dispatch` has a logic bug

**File:** `pkg/extproc/sdk/python/fir_ext.py:196-201`

The response detection branch checks `if msg_id is not None and "method" not in msg`. But JSON-RPC responses don't have a `method` field, so this works. However, it's checked **after** the method-based branches, so a response with id=5 won't match any `method` branch and falls through correctly. This is fragile — if a response somehow had a `method` key (non-standard), it would be misrouted.

Actually, this is fine for standard JSON-RPC. Downgrading concern.

---

### I9. No `session_shutdown` notification sent before `Stop`

**File:** `pkg/extproc/manager.go:103-112`

The design doc (§5) says "fir sends `event/session_shutdown` notification, then waits 2s before sending SIGTERM." `Manager.Stop()` just cancels and kills — no shutdown event is sent.

**Fix:** Call `mb.bridge.EmitEvent("session_shutdown", nil)` before cancelling.

---

### I10. Handshake uses hardcoded ID=1, conflicts with Bridge's ID space

**File:** `pkg/extproc/capability.go:35` + `pkg/extproc/bridge.go:29`

Handshake sends request with `id=1`. Bridge's `nextID` starts at 0 and increments with `Add(1)`, so its first request also gets `id=1`. If a hook call happens to use id=1, and a stale init response arrives, `routeResponse` would deliver it to the wrong caller.

In practice this is unlikely (handshake completes before `Run` starts), but it's a latent bug.

**Fix:** Start `Bridge.nextID` at 100, or use the handshake ID outside the Bridge's range.

---

## MINOR

### M1. `Handshake` timeout test is skipped

**File:** `pkg/extproc/capability_test.go:75`

`TestHandshake_Timeout` is `t.Skip`-ed with the comment "would take 5s". This leaves a critical path untested.

**Fix:** Make the timeout configurable (pass it as a parameter) and use a short timeout in tests.

---

### M2. `Discovery` test doesn't test actual global directory scanning

**File:** `pkg/extproc/discovery_test.go:54-55`

The test acknowledges it can't test `os.UserHomeDir`-based global discovery and uses a helper instead. `Discover()` itself is only tested for project-local dirs.

**Fix:** Make the global dir configurable (e.g., accept it as a parameter or use a package-level var for testing).

---

### M3. Manager tests use `time.Sleep(200ms)` for synchronization

**File:** `pkg/extproc/manager_test.go:48,84`

Sleeping 200ms to wait for async setup is flaky. Under load or slow CI, this may not be enough.

**Fix:** Use a channel or polling loop with a timeout.

---

### M4. `Process.Restart` has a `time.Sleep(backoff)` that holds no lock

**File:** `pkg/extproc/process.go:132`

`Restart` sleeps with backoff while holding no lock, which is fine for concurrency, but the sleep is unconditional even on the first restart attempt (1s delay). This means the first restart always waits 1s even if the process died instantly.

---

### M5. `WriteResponse` emits both `result` and `error` as `omitempty`

**File:** `pkg/extproc/jsonrpc.go:113-118`

When `result` is `nil` and `rpcErr` is non-nil, the `result` field is omitted (correct). But when `result` is non-nil and `rpcErr` is nil, the `error` field is omitted (also correct). However, a successful response with a `nil` result (e.g., hook returning null) omits the `result` field entirely, which violates JSON-RPC 2.0 spec (result MUST be present on success).

**Fix:** Use a pointer wrapper or custom marshaling to emit `"result": null` explicitly.

---

### M6. `stripExt` removes only one extension

**File:** `pkg/extproc/discovery.go:61-66`

`stripExt("my-ext.tar.gz")` returns `"my-ext.tar"`. This is probably fine for extension naming but worth noting.

---

### M7. Python SDK uses global mutable state for registries

**File:** `pkg/extproc/sdk/python/fir_ext.py:19-22`

`_tools`, `_tool_handlers`, etc. are module-level globals. Tests manually clear them in `setUp`. This is standard for a simple SDK but means `run()` can only be called once per process. The tests handle this correctly.

---

### M8. Python SDK `Context.exec` signature doesn't match Go handler

**File:** `pkg/extproc/sdk/python/fir_ext.py:144` vs `pkg/extproc/bridge.go:82`

Python sends `{"command": ..., "timeout": ...}`, Go expects `{"command": ..., "args": [...]}`. The `timeout` param is ignored on the Go side, and `args` is never sent by Python.

(Duplicate of I4, listed here for cross-reference.)

---

### M9. No validation of extension `name` from init response

**File:** `pkg/extproc/capability.go`

The extension can return any `name` in its init response, including empty string or names with special characters. This name is used in logging but could cause confusion if it conflicts with another extension's name.

---

### M10. `TrustStore` re-reads and re-writes the JSON file on every operation

**File:** `pkg/extproc/trust.go:68,76`

`IsTrusted` and `RecordTrust` both do a full file read + parse. For the expected scale (handful of extensions), this is fine, but it's worth noting.
