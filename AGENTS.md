Use idiomatic Go. Keep it simple.

Split large files into smaller ones, making sure to update the mapping in `sync/UPSTREAM_MAP.md`

See `PLAN.md` for the current plan and process.

Do not ignore any issues, address them promptly, even if preexisting. Do not postpone any work, even if it seems daunting - just break it down into smaller tasks.

Do not leave incomplete or stubbed code. Ensure all code is functional and tested.

## Git

Git commands that require an editor (e.g. `git rebase --continue`, `git commit`, `git merge --continue`) will open vim non-interactively and hang. Always prefix such commands with `GIT_EDITOR=true` to accept the default message without opening an editor:

```bash
GIT_EDITOR=true git rebase --continue
GIT_EDITOR=true git commit
```

## Testing

Run `make test` to verify your changes. Always finish every task with `make all` to confirm the full build and test suite passes.

## Extensions

Extensions are standalone scripts (Python, shell, etc.) that run as subprocesses
and communicate with fir over JSON-RPC 2.0 on stdio. They are discovered
automatically from `.fir/extensions/` in the project directory.

The extension system lives in `pkg/extproc/`. New extensions need no code
changes — just drop a `.py` or `.sh` file in `.fir/extensions/`.

Use `--no-extensions` to disable all extension discovery.

## Changelog

When making non-trivial changes, add an entry under `## [Unreleased]` in `CHANGELOG.md` using the appropriate subsection (`### Added`, `### Fixed`, `### Changed`, `### Removed`). Keep entries concise — one line per change. Do not bump `VERSION`; that happens during release.

New entries go at the top of their subsection (most recent first).

## Models

`pkg/ai/models_generated.go` is a generated file — **never edit it directly**. It is completely overwritten by `make generate-models`. Any manual edits will be lost.

To add or fix a model so it survives regeneration, add it to `applyOverridesAndAdditions()` in `cmd/generate-models/main.go`. Use a `!hasModel(all, provider, id)` guard so it doesn't create duplicates if the upstream API starts returning it.

## ACP Mode

ACP is a **standalone mode** (`pkg/modes/acp/`), not an extension. See `docs/plan/15-modes-as-extensions.md` for the full analysis of why.

Key constraints:
- Uses `github.com/coder/acp-go-sdk` for stable types + JSON-RPC transport.
- The Go SDK (schema 0.10.7) is missing unstable methods (`session/set_model`, `session/list`, `session/resume`); define those types locally in `types.go`.
- Use `acp.NewConnection` directly (not `AgentSideConnection`) to handle all methods — stable and unstable — in one switch.
