## Review — 2026-03-06

Files reviewed:
- `pkg/ai/types.go`
- `cmd/generate-models/main.go`
- `cmd/generate-models/main_test.go`
- `pkg/modes/interactive/components/model_selector.go`
- `pkg/ai/models_generated.go` (spot-checked)

---

_All items from initial review have been addressed. Remaining minor items below._

## Simplification

- `cmd/generate-models/main.go` — `sweLeaderboardPatterns` duplicates every model key 2–3 times with slight spelling variants ("claude opus 4.6", "claude-opus-4-6", "claude 4.6 opus"). This is ~70 lines of ordered entries where a missed ordering causes silent bugs. Consider normalising leaderboard names (strip punctuation, collapse whitespace) and matching against a smaller canonical set instead. (Low priority — the init() guard now catches ordering bugs at runtime.)
