## Review — 2026-03-06

Files reviewed: `pkg/agent/plan.go`, `pkg/agent/plan_test.go`, `pkg/core/agentsession.go`, `pkg/core/sdk.go`, `pkg/core/systemprompt.go`, `pkg/core/tools/plan.go`, `pkg/core/tools/plan_test.go`, `pkg/modes/acp/plan.go`, `pkg/modes/acp/plan_test.go`, `pkg/modes/acp/acp.go`

## Correctness

(No items.)

## Simplification

- `pkg/modes/acp/plan.go` — `planConn` interface duplicates the `SessionUpdate` method signature already on `acpConn`. The planTracker could just take `acpConn` directly instead of defining a narrower interface, since both are test-friendly. Minor either way. NOTE: it's a good style to be explicit about interface boundaries, so let's keep the interface as-is.

## Test Coverage

(No items.)
