# Design: extension CLI verbs

**Status**: design stub / future anchor. Not scheduled.
**Motivated by**: `docs/design/observe.md` discussion — the observe extension
could in principle register `fir observe` itself. Deferred deliberately
(see "Why not now").

## Goal

Let extensions register top-level CLI verbs so that, e.g., a `deploy`
extension makes `fir deploy …` work — discovered, dispatched, and helped by
fir, with the actual implementation living in the extension.

The win:

- Third-party extensions extend the CLI surface without a Go PR.
- Builtin extensions can fully own their user-facing verbs (no
  `cmd/fir/foo.go` shim needed).
- Single namespace, single binary, single help system, single completion
  source.

`git`'s `git-foo` mechanism is the obvious prior art.

## Why not now

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
