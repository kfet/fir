# Design: extension CLI verbs

**Status**: implemented — landed alongside the move of `fir observe` /
`fir send` into `observe.py` (the first real users).
**Original motivation**: `docs/design/observe.md` discussion — the observe
extension could in principle register `fir observe` itself.

## Goal

Let extensions register top-level CLI verbs so that, e.g., a `deploy`
extension makes `fir deploy …` work — discovered, dispatched, and helped by
fir, with the actual implementation living in the extension.

## What actually shipped (vs. the original sketch)

Two deviations from the "Sketch" section below worth knowing:

1. **Bridge owns stdio, not raw exec.** The original sketch said "exec, not
   JSON-RPC" with the verb getting a real TTY directly. We instead reuse the
   existing extension-manager pattern — bridge over stdin/stdout — and add
   helper notifications (`cli_stdout`, `cli_stderr`, `cli_stdin`,
   `cli_signal`) so the extension drives fir's real TTY through fir. Reason:
   verbs that want LLM/auth/MCP/settings infrastructure get all of fir's
   bridge methods for free, without inventing a parallel exec-mode protocol.
   Cost: ~one JSON-RPC notification per output line; fine for line-oriented
   verbs (observe, send) — pathological only for raw-mode TUIs (out of scope).

2. **Frontmatter is a simple comma-separated list.** No nested YAML — just
   `cli_verbs: foo, bar`. The extension dispatches internally on `verb` from
   `cli_invoke` params. Help text per verb is deferred until a verb actually
   wants it.

## Wire protocol

```
fir → ext  (request)        cli_invoke { verb, argv, cwd, stdin_is_tty,
                                          stdout_is_tty, stderr_is_tty }
fir → ext  (notification)   cli_stdin  { data: "...\n" } | { eof: true }
fir → ext  (notification)   cli_signal { name: "interrupt" | ... }
ext → fir  (notification)   cli_stdout { data: "..." }
ext → fir  (notification)   cli_stderr { data: "..." }
ext → fir  (response)       { exit_code: N }
```

The `cli_invoke` response's `exit_code` becomes fir's exit code. Standard
bridge methods (`notify`, `exec`, etc.) are answered with `method-not-found`
in verb mode since there is no Manager / session.

## Discovery & dispatch

- Frontmatter `cli_verbs:` is parsed at `fir` startup — no extension
  spawning required to know which verbs exist.
- Built-in fir subcommands (registry in `cmd/fir/subcommands.go`) are
  reserved and cannot be shadowed. Two extensions claiming the same verb
  is a fatal startup error.
- Lookup happens in `cmd/fir/cliverb.go` after the builtin-subcommand check
  and before normal `ParseArgs`.

## SDK (Python)

```python
@fir_ext.cli_verb("greet", summary="Say hello")
def greet(argv, host):
    who = argv[0] if argv else "world"
    host.println(f"hello {who}")
    return 0

@fir_ext.on_cli_signal
def _quit(name, host):
    if "interrupt" in name.lower(): os._exit(130)
```

`host` exposes `println` / `print` / `eprintln` / `eprint` / `readline` /
`stdin_lines` plus `argv`, `cwd`, `stdin_is_tty`, `stdout_is_tty`,
`stderr_is_tty`.

---

## Original deferral note (kept for context)



1. **n=1 is wrong-by-construction for an abstraction.** observe is one use
   case. We need ≥3 *unrelated* motivating extensions (e.g. doctor, bench,
   migrate) before the right protocol shape is visible. Designing on observe
   alone produces an abstraction that fits observe and chafes everything
   else.

2. **Real feature surface, deserves its own pass.** Verb-namespace collisions,
   help integration, completion, argv conventions, signal forwarding, exit
   codes, builtin-vs-user precedence, error rendering from Python tracebacks
   to fir-style stderr, security model for third-party extensions claiming
   verbs (today extensions are explicit opt-in; CLI verbs would be implicit).
   Each of these is a small decision; together they're a feature.

3. **Builtin LoC win is a wash.** observe.py is embedded in the fir binary at
   compile time. Whether its CLI dispatch lives in `cmd/fir/observe.go` or
   in observe.py's `--fir-cli` mode is the same compilation unit, same
   review surface, same release cadence. The big payoff is for *third-party*
   extensions, which is a different feature with different requirements.

4. **Reverse port is trivial.** When this lands, observe.py becomes its
   first user — port the dispatcher, ~150 LoC moved between files, no
   behaviour change.

5. **observe doesn't need it.** ~120 LoC in `cmd/fir/observe.go` is a fine
   v1.

## When to revisit

When ≥3 unrelated motivating cases exist *and* one of them isn't builtin —
i.e. a real third-party extension wants this. Revisiting earlier means
generalising on insufficient evidence.

## Sketch (north star, not spec)

### Declaration — frontmatter, not init handshake

CLI verbs must be discoverable *without* spawning the extension (the verb
runs from cold; we can't pay extension-startup cost for `fir foo`). So
declare verbs in extension frontmatter:

```python
# ---
# name: observe
# cli_verbs:
#   - name: observe
#     summary: "Tail and observe a running fir session"
#     args: "[<id-prefix>] [flags]"
#   - name: send
#     summary: "Send input to a running fir session"
#     args: "<id-prefix> [flags]"
# ---
```

fir parses this at startup (extends `pkg/extension/discovery.go`), builds a
`verb → (extension-path, interpreter)` map, caches it.

### Dispatch — exec, not JSON-RPC

The extension is invoked as a normal subprocess with raw stdio. No bridge,
no notifications, no event stream:

```go
// cmd/fir/main.go, before normal arg parsing
if ext, verb := lookupCLIVerb(os.Args[1]); ext != nil {
    cmd := exec.Command(ext.Interpreter, ext.Path,
        append([]string{"--fir-cli", verb}, os.Args[2:]...)...)
    cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
    cmd.Env = append(os.Environ(),
        "FIR_AGENT_DIR=" + agentDir,
        "FIR_VERSION="   + version,
        "FIR_BINARY="    + selfPath,    // for re-exec'ing fir itself
    )
    cmd.Run()
    os.Exit(extractExitCode(cmd.ProcessState))
}
```

The extension receives:
- argv with `--fir-cli <verb>` sentinel + user args.
- TTY stdin/stdout/stderr (no JSON-RPC framing).
- Env vars for filesystem-level discovery (sidecars, sockets, agent dir).
- **No live bridge to fir.** Verbs run from cold; there is no
  `AgentSession` in this process to bridge to.

### Why no live bridge

If a verb needs to talk to a *running* fir, it does so via filesystem —
exactly like `fir observe` does (sidecars + sockets). That pattern is
already in `docs/design/observe.md` and is reusable for any future verb.
Adding a JSON-RPC live channel here would conflate "CLI verb" with
"plugin running inside a session," which are different concerns.

### Conflict resolution

- fir's own verbs (`run`, `pty`, `sessions`, `login`, `observe`, `send`,
  …) are **reserved** and cannot be shadowed.
- Two extensions declaring the same verb → fir refuses to start with a
  clear error naming both extensions. User picks one to disable.
- Precedence on lookup (when extensions are co-installed but not
  conflicting): builtin > project-local > global. Documented; never
  silent.

### Help integration

```
fir help                          # lists builtin + extension verbs separately
fir help <ext-verb>               # execs ext with --fir-cli <verb> --help;
                                  # ext owns its help text
```

`fir help` itself stays fast — reads frontmatter, doesn't spawn.

### Completion

Optional in frontmatter:

```yaml
cli_verbs:
  - name: observe
    completion: true        # fir runs ext with --fir-cli-complete <args...>
                            # ext prints one completion per line
```

Default off — verbs work without completion. Completion fires only on
`<TAB>` so spawn cost is acceptable.

### Exit codes & errors

Exit code propagates from extension to fir's caller verbatim. Python
tracebacks on stderr land as-is (fir does not reformat). Extensions are
expected to handle their own error-rendering; fir is a dispatcher, not a
parent process pretending to own the failure.

### Signal forwarding

`SIGINT`, `SIGTERM`, `SIGHUP`, `SIGQUIT` propagate from fir to the
extension subprocess via `cmd.Process.Signal`. fir waits for the
subprocess to exit; doesn't translate signals. Standard Unix shape.

### Security model

Today: extensions are explicit opt-in (`-e ext` or builtin / project-local
discovery). CLI verbs would inherit the *same* discovery rules — an
extension that's not loaded for sessions also doesn't claim verbs.
Importantly: `--no-extensions` disables verb dispatch as well.

Third-party packages installed via `fir install` and not yet trusted: they
do not claim verbs until the user explicitly enables them. **Verb
claiming is not implicit on install.** Documented as the security
boundary.

## Estimated cost when we build it

| Component | LoC |
|---|---|
| Frontmatter parse: add `cli_verbs` | ~30 |
| Verb-table builder + conflict detection at startup | ~50 |
| Dispatch shim in `cmd/fir/main.go` | ~40 |
| `fir help` integration | ~30 |
| Completion plumbing | ~50 |
| Reserved-verbs list + tests | ~20 |
| Docs (extension-protocol.md, fir_ext.py SDK, this doc → spec) | ~30 |
| **Total core** | **~250 LoC** |

Plus per-extension: each extension that wants verbs adds frontmatter and a
`--fir-cli` argv-parsing branch. observe.py would be the first user
(~30 LoC of dispatch glue inside the extension).

## Open questions to resolve when we revisit

- **Should verbs nest?** `fir observe send` vs `fir observe` + `fir send` as
  flat siblings. observe wants flat (today's design); future verbs may
  want nesting.
- **State sharing across invocations.** A verb may want to cache state
  across runs. Today extensions have `config_dirs`; should verbs use the
  same dirs or a new bucket? Probably same.
- **Discovery cost at every fir invocation.** Frontmatter parse is cheap
  but non-zero across N extensions. Consider caching in
  `~/.cache/fir/verb-table.json` keyed by extension mtimes.
- **Cross-platform.** Windows users running `fir.exe observe` — extensions
  use `python3` shebang or explicit interpreter; verb dispatch needs a
  Windows-friendly fallback. Same problem extensions already have.
- **Versioning.** If an extension's verb signature changes between fir
  versions, how is that surfaced? Probably out of scope (extensions are
  rebuilt with fir today), but worth re-checking.

## Anti-goals

- **Plugin frameworks à la Vim/IDEs.** This is verb dispatch only — not a
  general "extensions can hook anywhere in fir's CLI" mechanism. Hooks for
  global flags, pre-command middleware, output filters, etc. are
  out of scope. Keep the surface small.
- **Live coupling between verb and session.** A verb is a CLI command, not
  a session participant. Verbs that want session interaction use
  filesystem IPC (sidecars + sockets) like everything else.
