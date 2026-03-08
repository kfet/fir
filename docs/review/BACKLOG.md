## Review — 2026-03-07

**Files reviewed:** cmd/fir/app.go, cmd/fir/login.go, cmd/fir/CHANGELOG.md, Makefile, pkg/core/agentsession.go, pkg/core/session.go, pkg/core/sdk.go, pkg/core/slashcmds.go, pkg/core/modelregistry.go, pkg/extension/setup.go, pkg/modes/interactive/mode.go, pkg/core/builtin_extensions/tmuxspinner.py, pkg/config/defaults.go, pkg/auth/authstorage.go, pkg/config/settings.go, pkg/config/configvalue.go

## Correctness

- `cmd/fir/CHANGELOG.md` — `make publish` and `make deploy` listed under "Removed" but they are actually new additions in the Makefile. **Filed as URGENT.**

## Simplification

_(none found — the refactoring itself is a simplification, extracting `pkg/auth` and `pkg/config` from `pkg/core`)_

## Security

_(none found — no new secret handling, no path traversal, no injection vectors)_

## Test Coverage

_(existing tests were updated to use the new package paths; no new untested code paths identified)_
