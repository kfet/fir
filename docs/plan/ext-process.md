# External Process Extensions — Design Doc

## Overview

External process extensions are standalone executables that communicate with fir over JSON-RPC 2.0 on stdio. They complement the existing compiled-in extension system (`pkg/extension/`) by allowing users to write extensions in any language without recompiling fir.

## 1. Discovery

Fir scans two directories for executable files at startup:

- **Project-local:** `.fir/extensions/` (relative to project root)
- **Global:** `~/.config/fir/extensions/`

Each executable file (or symlink) in these directories is a candidate extension. The extension name is derived from the filename (sans extension). Subdirectories are ignored. Files must have the executable bit set.

Global extensions load first; project-local extensions load second and can shadow globals by name.

## 2. Wire Protocol

Communication uses **JSON-RPC 2.0** over stdin/stdout of the child process. Stderr from the child is captured and forwarded to fir's log.

### 2.1 Init Handshake

On spawn, fir sends an `init` request:

```json
{"jsonrpc":"2.0","id":1,"method":"init","params":{"version":"1","cwd":"/path/to/project"}}
```

The extension replies with its capabilities:

```json
{
  "jsonrpc":"2.0","id":1,"result":{
    "name":"my-ext",
    "tools":[
      {"name":"my_tool","description":"Does a thing","parameters":{"type":"object","properties":{"arg":{"type":"string"}}}}
    ],
    "events":["session_start","session_shutdown","tool_call","turn_end"]
  }
}
```

If the extension doesn't respond within 5 seconds, fir kills it and logs a warning.

### 2.2 Sync Hooks (fir → extension, waits for response)

These are JSON-RPC **requests** where fir blocks until the extension replies (with timeout):

| Method | Timeout | Description |
|---|---|---|
| `tool_call` | 30s | Extension-registered tool execution. Params include `tool_call_id`, `name`, `params`. |
| `hook/tool_call` | 5s | Intercept any tool call. Return `{"block":true,"reason":"..."}` or `null`. |
| `hook/before_agent_start` | 5s | Modify system prompt or inject a message before agent runs. |
| `hook/input` | 5s | Transform or handle user input. Return `{"action":"transform","text":"..."}` or `null`. |

### 2.3 Async Notifications (fir → extension, fire-and-forget)

These are JSON-RPC **notifications** (no `id`, no response expected):

`event/session_start`, `event/session_shutdown`, `event/agent_start`, `event/agent_end`, `event/turn_start`, `event/turn_end`, `event/message_start`, `event/message_end`, `event/tool_execution_start`, `event/tool_execution_end`, `event/tool_result`, `event/model_select`.

Only events the extension subscribed to in `init` are sent.

### 2.4 Extension → Fir Calls

The extension can send JSON-RPC requests to fir (on the same stdio channel):

| Method | Description |
|---|---|
| `send_message` | Inject a custom message into the session |
| `send_user_message` | Send a user message to the agent |
| `set_session_name` | Set session display name |
| `set_label` / `clear_label` | Set/clear label on an entry |
| `get_active_tools` | Returns list of active tool names |
| `set_active_tools` | Set which tools are active |
| `set_model` | Change the current model |
| `notify` | Show a notification (level: info/warning/error) |
| `set_status` | Set persistent status text in footer |
| `exec` | Run a shell command, returns stdout/stderr/exit_code |

## 3. Reduced API Surface

The following compiled-in API methods are **not exposed** to external extensions:

- `RegisterShortcut` — requires in-process key handling
- `RegisterFlag` — must be known at CLI parse time
- `RegisterCommand` — complex CommandContext lifecycle
- `SetWidget` / `ClearWidget` — high-frequency UI coupling
- `GetThinkingLevel` / `SetThinkingLevel` — internal model detail
- `WaitForIdle` / `NewSession` / `Fork` / `Reload` / `Shutdown` — session control too dangerous for external processes

## 4. SDK Injection

Fir embeds lightweight SDK stubs for Python, Node.js, and Ruby using `embed.FS`. On first run (or version mismatch), it extracts them to `~/.cache/fir/sdks/<version>/{python,node,ruby}/`.

Before spawning an extension process, fir prepends the appropriate path to environment variables:

- `PYTHONPATH` → `~/.cache/fir/sdks/<ver>/python`
- `NODE_PATH` → `~/.cache/fir/sdks/<ver>/node`
- `RUBYLIB` → `~/.cache/fir/sdks/<ver>/ruby`

Each SDK provides a thin wrapper that handles JSON-RPC framing on stdio so extension authors write simple handler functions, not protocol code.

## 5. Process Lifecycle

1. **Spawn:** On session start, fir spawns each discovered extension as a child process.
2. **Init:** Fir sends `init`, waits up to 5s for a response. Failure = skip extension.
3. **Run:** The process stays alive for the duration of the session.
4. **Crash restart:** If the process exits unexpectedly, fir restarts it with exponential backoff (1s, 2s, 4s, max 30s). After 5 consecutive failures, the extension is disabled for the session.
5. **Shutdown:** On session end, fir sends `event/session_shutdown` notification, then waits 2s before sending SIGTERM, then 1s before SIGKILL.
6. **Timeouts:** Hook requests that exceed their timeout are abandoned (fir proceeds as if the extension returned `null`). The extension process is not killed for a single timeout, but 3 consecutive timeouts trigger a restart.

## 6. Security

**Project-local extensions** (`.fir/extensions/`) are untrusted by default. On first encounter of a new project-local extension (or when its content hash changes), fir prompts the user for confirmation before executing it. Approval is persisted in `~/.config/fir/trusted-extensions.json` keyed by `(project_path, extension_name, sha256)`.

**Global extensions** (`~/.config/fir/extensions/`) are trusted implicitly — the user placed them there intentionally.

---

## Implementation Tasks

1. **Discovery & config types** — Create `pkg/extproc/discovery.go`. Define `ExtProcConfig` (name, path, scope enum project/global). Implement `Discover(projectDir string) ([]ExtProcConfig, error)` scanning both directories. No dependencies.

2. **JSON-RPC codec** — Create `pkg/extproc/jsonrpc.go`. Define `Request`, `Response`, `Notification` structs. Implement `Codec` that reads/writes newline-delimited JSON-RPC 2.0 over `io.ReadWriter`. Implement `ReadMessage`, `WriteRequest`, `WriteNotification`, `WriteResponse`. No dependencies.

3. **Process manager** — Create `pkg/extproc/process.go`. Define `Process` struct (wraps `exec.Cmd`, holds `Codec`, stderr pipe → log). Implement `Start(cfg ExtProcConfig, env []string) error`, `Stop(ctx context.Context) error`, `Restart() error` with backoff. Depends on task 2.

4. **Init handshake & capability types** — Create `pkg/extproc/capability.go`. Define `InitParams`, `InitResult` (tools, events). Implement `Handshake(proc *Process) (*InitResult, error)` with 5s timeout. Depends on tasks 2–3.

5. **Extension bridge** — Create `pkg/extproc/bridge.go`. Define `Bridge` struct that implements a subset of `extension.API` by proxying calls over JSON-RPC to the child. Register tools from `InitResult`. Subscribe to events declared by the extension. Handle inbound requests from extension (notify, exec, send_message, etc.) via a dispatch loop in `Bridge.Run()`. Depends on tasks 2–4. Types: `Bridge`, `Run(ctx context.Context) error`, `EmitEvent(name string, data any)`, `CallHook(name string, data any, timeout time.Duration) (json.RawMessage, error)`.

6. **Runner integration** — Modify `pkg/extproc/manager.go` (new file) and `pkg/extension/integration.go`. Define `Manager` that owns all `Bridge` instances. `Manager.Start(session, runner)` discovers, spawns, handshakes all ext-procs, registers their tools/events on the `extension.Runner`. `Manager.Stop()` shuts them all down. Wire `Manager` into `extension.Setup()`. Depends on tasks 1–5.

7. **SDK embedding** — Create `pkg/extproc/sdk/` with `embed.go` (uses `//go:embed` for `python/`, `node/`, `ruby/` dirs), `extract.go` (`EnsureExtracted() (string, error)` — extracts to `~/.cache/fir/sdks/<ver>/` if missing or stale). Create minimal Python SDK: `sdk/python/fir_ext.py` with `run(on_init, handlers)` helper. No code dependencies.

8. **Security — trust prompt** — Create `pkg/extproc/trust.go`. Define `TrustStore` (reads/writes `~/.config/fir/trusted-extensions.json`). Implement `IsTrusted(projectDir, name, hash string) bool`, `RecordTrust(...)`, `ComputeHash(path string) (string, error)`. Integrate into `Manager.Start()` — skip untrusted project-local extensions (prompt via `UIContext.Confirm` if available, reject silently in non-interactive modes). Depends on task 6.

9. **Tests** — Create `pkg/extproc/discovery_test.go`, `jsonrpc_test.go`, `process_test.go`, `bridge_test.go`, `manager_test.go`. Use a simple test extension script (bash or Go test binary) that speaks the protocol. Aim for coverage of: discovery logic, codec round-trip, init handshake timeout, tool call dispatch, event filtering, crash restart with backoff, trust store persistence.

10. **Documentation & changelog** — Add `docs/extensions.md` user guide (how to write an external extension, protocol reference, SDK usage). Update `CHANGELOG.md` with the feature entry. Update `AGENTS.md` if new conventions arise.

Tasks 1, 2, 7 are independent and can run in parallel. Tasks 3–4 are sequential. Task 5 depends on 2–4. Task 6 depends on 1+5. Tasks 8–10 depend on 6.
