## Review — 2026-03-06

Files reviewed: `pkg/core/agentsession.go`, `pkg/core/agentsession_test.go`, `pkg/core/keybindings.go`, `pkg/modes/interactive/components/footer.go`, `pkg/modes/interactive/components/footer_test.go`, `pkg/modes/interactive/mode.go`

## Simplification

- `pkg/modes/acp/plan.go` — `planConn` interface duplicates the `SessionUpdate` method signature already on `acpConn`. Minor, keeping as-is per previous review.

## Test Coverage

(No items.)

## Correctness

(No items.)
