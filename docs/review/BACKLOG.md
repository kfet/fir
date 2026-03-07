# Review Backlog

## 2026-03-06 — Reviewed: pkg/modes/interactive/mode.go, pkg/core/slashcmds.go (/update command)

No issues found.

## 2026-03-06 — Reviewed: pkg/ai/providers/openai_responses.go

### ~~Simplification~~ (FIXED)
- ~~`pkg/ai/providers/openai_responses.go:218,290` — duplicated `supportsImage` check~~ → Extracted `modelSupportsImage()` helper.
