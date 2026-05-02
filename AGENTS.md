Use idiomatic Go. Keep it simple.

Prefer `sync/atomic`, `sync.Once`, and channels over manual mutex management when appropriate.

`.fir/skills` is a symlink to `pkg/core/builtin_skills/`. They are the same directory — don't treat them as separate copies.

## Python 3.9 minimum

All Python code (SDK, extensions, tests) **must** stay compatible with Python 3.9 — macOS ships 3.9 and fir must work on a fresh install with no Homebrew/pyenv. Do not bump `requires-python` in `pyproject.toml` or `python-version` in the ty config.

Never dismiss a problem as "pre-existing" or "out of scope" — you own this entire codebase. If you see it, fix it. No stubs, no TODOs, no postponed work; break large tasks into small ones instead.

## Modes — think before you specialise

Fir has multiple modes (CLI, ACP, etc. under `pkg/modes/`). Before implementing a fix or feature inside a specific mode, stop and ask: **is this actually unique to this mode, or does it belong in core?**

- If the behaviour is useful across modes, put it in core (`pkg/core/`, `pkg/ai/`, etc.) and have each mode reuse it.
- Only put logic inside a mode package when it is genuinely mode-specific (e.g. ACP JSON-RPC handling, CLI-only flags).
- When fixing a bug found in one mode, check whether the same bug exists in other modes. Fix it at the root, not per-mode.

When adding or changing user-visible features (CLI flags, subcommands, slash commands, settings), update the `self` skill (`.fir/skills/self/SKILL.md`) to keep it accurate.

## Skills vs Extensions vs Core

Before implementing any new feature or refactoring existing behaviour, ask: **does this need to live in core?**

- **Skill** — pure prompt/instruction content that guides the AI. Use for workflows, personas, task recipes, or domain knowledge that requires no code. Zero compilation, zero risk.
- **Extension** — a standalone script (Python/shell) that adds tools or reacts to events over JSON-RPC. Use for integrations, automation, or capabilities that a script can handle without touching the Go codebase.
- **Core (Go)** — only for things that genuinely require it: new transport protocols, performance-critical paths, first-class CLI flags, or capabilities that skills/extensions cannot express.

Default to a skill. If it needs to call external APIs or run commands, make it an extension. Only reach for core changes when skills and extensions are truly insufficient.

## Git

Git commands that require an editor (e.g. `git rebase --continue`, `git commit`, `git merge --continue`) will open vim non-interactively and hang. Always prefix such commands with `GIT_EDITOR=true` to accept the default message without opening an editor:

```bash
GIT_EDITOR=true git rebase --continue
GIT_EDITOR=true git commit
```

When the user says "rebase to main", they mean local `main`, not `origin/main`.

When merging a feature branch back to main, always use `git merge --ff-only` to keep a linear history and avoid merge commits. Rebase the branch first if needed.

## Worktrees

All non-trivial work happens in a **git worktree** on a feature branch. Never edit `main` directly.

```bash
FEATURE="<short-kebab-name>"          # e.g. acp-auth-methods
BRANCH="work/${FEATURE}"
PROJECT="$PWD"                        # captured before we cd away
WORKTREE="${PROJECT}-wt-${FEATURE}"   # sibling of project root

git worktree add "$WORKTREE" -b "$BRANCH"
cd "$WORKTREE"
```

All edits, tests, and commits happen in `$WORKTREE`.

If the task needs design work, write a short plan doc **in the worktree** before coding — name specific files, interfaces, and test cases.

When the task touches multiple packages or wants parallel work streams, use the `shepherd` skill to coordinate multiple agents (it reuses this worktree convention).

To delegate a task to a fresh agent in a new tmux window instead of doing it yourself, use the `wt` skill.

### Finishing

1. Final `make all` in the worktree.
2. Call the advisor (`aside` with `escalate=true`) before declaring the task done — see *Advisor* below.
3. Commit everything.
4. Ff-merge back to main and confirm with the user if they want to do a clean up, since it's destructive (using the captured `$PROJECT`, since you're still inside `$WORKTREE`):
   ```bash
   git -C "$PROJECT" merge --ff-only "$BRANCH"
   git -C "$PROJECT" worktree remove "$WORKTREE"
   git -C "$PROJECT" branch -d "$BRANCH"
   ```
## Advisor

The `aside-advisor` skill (auto-loaded) explains how to escalate to a stronger advisor model via `aside` with `escalate=true`. Use it at the high-leverage moments:

- **Before committing to an approach** on any non-trivial task — after orientation, before substantive edits.
- **When stuck** — recurring errors, not converging, results that don't fit. If you've re-run the same command 5+ times (see *Stuck loops*), escalate instead of looping.
- **Before declaring a task done** — after `make all` passes and the deliverable is written, but before you tell the user it's finished.
- **When considering a change of approach.**

Apply the bottleneck check first: if more data would resolve the uncertainty, gather it; only escalate when the gap is reasoning, not information.

## Stuck loops

If you have run the same command (e.g. `go test`, `go build`) more than 5 times
since your last file edit, you are in a stuck loop. Stop. Do not read any file
you have already read this session. Rewrite the problematic file completely from
scratch. If tests are failing due to API changes, the test file itself needs
updating — patch it or rewrite it, don't just re-run it.

## Build and test

Run `make test` to verify your changes. Always finish every task with `make all` to confirm the full build and test suite passes.

When fixing a regression, **write the test first** so it fails before your fix, then make it pass. This confirms the test actually catches the bug.

## Testing — avoid wall-clock timeouts

Prefer deterministic synchronization over `time.Sleep` and wall-clock polling:

- **Channels over polling** — use `chan struct{}` signals, `sync.WaitGroup`, or callbacks instead of `require.Eventually` with arbitrary timeouts. When testing async behaviour (reconnects, event delivery), wire callbacks or subscribe to events and wait on channels.
- **No `time.Sleep` in tests** — sleep-based tests are flaky under CI load and the race detector. If you need to wait for a goroutine, use a channel or sync primitive.
- **`require.Eventually` is a last resort** — only for checking external state you can't subscribe to (e.g. polling a server's registration map). Use short poll intervals (10ms) and generous timeouts (3-5s) when unavoidable.
- **Callbacks in Config, not after init** — if a struct spawns goroutines on creation, callbacks must be set via the config/options struct *before* construction, not after. Setting callbacks after init races with the goroutine.

## Extensions

Extensions are standalone scripts (Python, shell, etc.) that run as subprocesses
and communicate with fir over JSON-RPC 2.0 on stdio. They are discovered
automatically from `.fir/extensions/` in the project directory.

The extension system lives in `pkg/extension/`. New extensions need no code
changes — just drop a `.py` or `.sh` file in `.fir/extensions/`.

Every extension script **must** have a comment frontmatter block (`# ---` … `# ---`) with at least a `name` key. Files without frontmatter are ignored by discovery.

**Never place test files in the extensions directory** (`pkg/resources/builtin_extensions/` or `.fir/extensions/`). Extension tests belong in `pkg/resources/testdata/` (Python unit tests) or `pkg/extension/integration/` (Go integration tests). Files in the extensions directory are embedded and discovered at runtime — test files there cause 5-second handshake timeouts on every startup.

**Keep extension docs in sync.** When you add, remove, or change a bridge method, SDK function, event, hook, or context method:

- Update `demo.py` (`pkg/resources/builtin_extensions/demo.py`) to exercise it and update its companion test (`pkg/extension/sdk/python/demo_ext_test.py`). The demo must always reflect the current API — it is the living documentation.
- Update `docs/extension-protocol.md` — the wire-protocol reference (message shapes, params, return values, timeouts).
- Update the module docstring in `pkg/extension/sdk/python/fir_ext.py` — it mirrors the protocol reference for SDK users reading source.

You can also only enable specific extensions per-invocation with `--extension` / `-e`,or disable all of them with `--no-extensions`:

```bash
fir -e demo -e hello "do something"   # only these two
fir --no-extensions "do something"     # none at all
```

## Changelog

When making non-trivial changes, add an entry under `## [Unreleased]` in `CHANGELOG.md` using the appropriate subsection (`### Added`, `### Fixed`, `### Changed`, `### Removed`). Keep entries concise — one line per change. Do not bump `VERSION`; that happens during release.

New entries go at the top of their subsection (most recent first).

## Models

`pkg/ai/models_generated.go` is a generated file — **never edit it directly**. It is completely overwritten by `make generate-models`. Any manual edits will be lost.

To add or fix a model so it survives regeneration, add it to `applyOverridesAndAdditions()` in `cmd/generate-models/main.go`. Use a `!hasModel(all, provider, id)` guard so it doesn't create duplicates if the upstream API starts returning it.

## ACP Mode

ACP is a **standalone mode** (`pkg/modes/acp/`), not an extension.

Key constraints:
- Uses `github.com/coder/acp-go-sdk` for stable types + JSON-RPC transport.
- The Go SDK (schema 0.10.7) is missing unstable methods (`session/set_model`, `session/list`, `session/resume`); define those types locally in `types.go`.
- Use `acp.NewConnection` directly (not `AgentSideConnection`) to handle all methods — stable and unstable — in one switch.

