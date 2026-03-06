## Review — 2026-03-06

Files reviewed: `cmd/generate-models/main.go`, `pkg/ai/models_generated.go`, `cmd/fir/CHANGELOG.md`, `pkg/core/builtin_skills/work/SKILL.md`

## Correctness

(No items.)

## Simplification

- `pkg/modes/acp/plan.go` — `planConn` interface duplicates the `SessionUpdate` method signature already on `acpConn`. Minor, keeping as-is per previous review.

## Test Coverage

(No items.)
