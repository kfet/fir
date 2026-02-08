# Agent Resume Guide

When an agent picks up this project with clean context, follow this sequence:

## 1. Orient

```
Read docs/plan/01-overview.md     # What is this project
Read docs/plan/07-work-tracker.md # What's done, what's next
```

## 2. Find the next task

Look for the first `[ ]` item in `07-work-tracker.md`. That's what to work on.

## 3. Find the source

Each tracker item has the format:
```
- [ ] `pkg/ai/types.go` ← `packages/ai/src/types.ts` (295 lines)
```

The right side is the TS source path relative to `../pi-mono/`. Read it:
```
Read ../pi-mono/packages/ai/src/types.ts
```

## 4. Check for patterns

If other files in the same Go package are already ported, read one to match style:
```
ls pkg/ai/
Read pkg/ai/eventstream.go   # See how the first file was done
```

## 5. Port the file

Create the Go file. Include the header:
```go
// Ported from: packages/ai/src/types.ts
// Upstream hash: 1caadb2e
package ai
```

## 6. Update tracker

Change `[ ]` to `[x]` in `07-work-tracker.md`.

## 7. Test

```bash
make test       # Run tests
make build      # Verify compilation
make build-all  # Cross-compile all targets
```

## Key Reference Files

| Need to understand... | Read this TS file |
|---|---|
| Message/Model types | `packages/ai/src/types.ts` |
| How streaming works | `packages/ai/src/utils/event-stream.ts` |
| How agent loop works | `packages/agent/src/agent-loop.ts` |
| How tools are defined | `packages/coding-agent/src/core/tools/read.ts` (simplest) |
| How session persistence works | `packages/coding-agent/src/core/session-manager.ts` |
| How everything wires together | `packages/coding-agent/src/core/sdk.ts` |
| The main entry point | `packages/coding-agent/src/main.ts` |

## TS Source Location

The upstream TS code is at `../pi-mono/` relative to this repo's root.

If that path doesn't exist, clone it:
```bash
cd .. && git clone https://github.com/badlogic/pi-mono.git
```
