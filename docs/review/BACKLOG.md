## Review — 2026-02-28

Files reviewed: pkg/log/log.go, pkg/log/log_test.go, cmd/fir/app.go, cmd/fir/args.go,
pkg/agent/loop.go, pkg/core/agentsession.go, pkg/core/tools/bash.go,
pkg/ai/providers/anthropic.go, pkg/extension/runner.go, pkg/modes/rpc/server.go,
pkg/modes/acp/acp.go, pkg/modes/print/print.go (+ spot-checked all 25 instrumented files)

## Simplification

- `cmd/fir/app.go:273-274` — The `if debugEnabled { firlog.Info(...); firlog.Debug(...) }` guard is redundant. When disabled, `firlog.Info/Debug` are already zero-cost no-ops via `discardHandler.Enabled()`. Remove the guard for consistency with all other call sites.

- `pkg/log/log.go:24` — The `mu sync.Mutex` guards `Init` but the comment says "we only assign logger once". If `Init` is truly one-shot, consider using `sync.Once` instead — simpler and communicates intent. If multiple `Init` calls should be supported (e.g. test reset), keep the mutex but document that.

## Test Coverage

- `pkg/log/log_test.go` — Good coverage (6 tests). Missing: test that concurrent `Debug` calls during/after `Init` don't race. Add a test with `go test -race` verification (call `Init` from one goroutine while logging from another).

- `pkg/log/log_test.go` — `resetLogger()` directly sets the package-level `logger` var. This means tests are not parallel-safe (`t.Parallel()` would race). Fine for now since tests are sequential, but worth a comment.

## Correctness

(none — all items fixed)
