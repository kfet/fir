# Agent Resume Guide

You may be working alongside other agents. Be aware of that.

## 1. Orient

```
Read docs/plan/01-overview.md     # What is this project
Read docs/plan/07-work-tracker.md # What's done, what's next
```

## 2. Check What Others Are Doing

```bash
# Files modified in the last 10 minutes = someone is probably active there
find pkg/ cmd/ -name "*.go" -mmin -10 2>/dev/null
```

## 3. Pick a Task

Find `[ ]` items in `07-work-tracker.md` where:
- Dependencies are `[x]` (the Go files exist and compile)
- No one is actively editing nearby files (see step 2)
- Use `touch` to mark your starting work on a file, after you've checked it was not already touched by someone else in the last 10 minutes

## 4. Read the TS Source

Each row has the TS path relative to `../pi-mono/packages/`:
```
Read ../pi-mono/packages/ai/src/types.ts
```

If other Go files in the package exist, read one for style consistency.

## 5. Port and Test

Write `foo.go` + `foo_test.go`. Include the header:
```go
// Ported from: packages/ai/src/types.ts
// Upstream hash: 1caadb2e
package ai
```

Tests must not call real APIs. Use `httptest.Server` with fixture data.
See `docs/plan/10-testing.md` for patterns.

## 6. Verify

```bash
go vet ./pkg/ai/...
go test ./pkg/ai/...
make build
```

## 7. Mark Done

**Re-read `07-work-tracker.md` first** (another agent may have updated it).
Then change `[ ]` to `[x]` for your task. Pick the next one.

## Key TS Reference Files

| Need to understand... | Read |
|---|---|
| Message/Model types | `packages/ai/src/types.ts` |
| How streaming works | `packages/ai/src/utils/event-stream.ts` |
| How agent loop works | `packages/agent/src/agent-loop.ts` |
| How tools are defined | `packages/coding-agent/src/core/tools/read.ts` |
| How sessions work | `packages/coding-agent/src/core/session-manager.ts` |
| How it wires together | `packages/coding-agent/src/core/sdk.ts` |
| Main entry point | `packages/coding-agent/src/main.ts` |
| OAuth types & flows | `packages/ai/src/utils/oauth/types.ts` + provider files |
| Testing patterns | `docs/plan/10-testing.md` |

## TS Source Location

`../pi-mono/` relative to repo root. If missing:
```bash
cd .. && git clone https://github.com/badlogic/pi-mono.git
```
