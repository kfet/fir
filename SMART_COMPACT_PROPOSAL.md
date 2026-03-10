# Flexible Auto-Compaction Triggers

## Goal

Replace the hardcoded 90% fill-ratio compaction trigger with configurable, multi-signal triggers: adjustable fill ratio, absolute token cap, and cost-based cap.

## Background

Currently in `pkg/core/compaction/compaction.go`, `ShouldCompact` uses a hardcoded `minFillRatio = 0.90`. This is suboptimal because:
- On expensive models, 90% of a 200K window means ~180K input tokens per turn ($2.70/turn on Opus).
- On models with huge context windows (1M+), 90% means ~900K tokens — extremely slow and expensive.
- Users have no way to tune when compaction fires.

## Design Notes

### Trigger semantics (OR)

Compaction fires if **any** trigger matches. A user setting `fillRatio: 0.9` + `maxContextCost: 0.50` gets whichever fires first. All triggers are independent — there is no priority or combining logic.

### ReserveTokens interaction

The `MaxContextTokens` and `MaxContextCost` triggers bypass the existing `ReserveTokens` guard. This is intentional: those triggers represent hard caps the user explicitly set, so they should fire unconditionally. The `ReserveTokens` check only gates the fill-ratio path, preserving existing behavior.

### Zero-value semantics for FillRatio

A `FillRatio` of `0` means "use the default (0.90)," not "disable ratio-based compaction." To disable ratio-based compaction, set `FillRatio` to a value > 1.0 (e.g., `2.0`). This is consistent with the pointer-optional config layer: an unset field gets the zero value, which should mean "default."

### Why `inputCostPerMTok` instead of full `ModelCost`

Only input cost matters for the compaction decision (we're measuring how expensive the *context* is). Passing the full struct would be more future-proof but adds coupling for no current benefit. If output cost awareness is needed later, the signature can be extended.

## Changes

### 1. Add new fields to `CompactionSettings` (in two places)

**`pkg/core/compaction/compaction.go`** — the compaction-local struct:
```go
type CompactionSettings struct {
    Enabled          bool    `json:"enabled"`
    ReserveTokens    int     `json:"reserveTokens"`
    KeepRecentTokens int     `json:"keepRecentTokens"`
    FillRatio        float64 `json:"fillRatio"`        // 0.0–1.0, default 0.90
    MaxContextTokens int     `json:"maxContextTokens"` // absolute cap, 0 = disabled
    MaxContextCost   float64 `json:"maxContextCost"`   // USD per turn, 0 = disabled
}
```

**`pkg/config/settings.go`** — the user-facing config struct (uses pointers for optionality):
```go
type CompactionSettings struct {
    Enabled          *bool    `json:"enabled,omitempty"`
    ReserveTokens    *int     `json:"reserveTokens,omitempty"`
    KeepRecentTokens *int     `json:"keepRecentTokens,omitempty"`
    FillRatio        *float64 `json:"fillRatio,omitempty"`
    MaxContextTokens *int     `json:"maxContextTokens,omitempty"`
    MaxContextCost   *float64 `json:"maxContextCost,omitempty"`
}
```

### 2. Update `ShouldCompact` function

**File:** `pkg/core/compaction/compaction.go`, line ~177

Change the signature to accept model cost info:

```go
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings, inputCostPerMTok float64) bool {
    if !settings.Enabled || contextWindow <= 0 {
        return false
    }

    // Absolute token cap (useful for huge-window models)
    if settings.MaxContextTokens > 0 && contextTokens > settings.MaxContextTokens {
        return true
    }

    // Cost cap: compact when input cost per turn exceeds threshold
    if settings.MaxContextCost > 0 && inputCostPerMTok > 0 {
        costUSD := float64(contextTokens) * inputCostPerMTok / 1_000_000
        if costUSD > settings.MaxContextCost {
            return true
        }
    }

    // Fill ratio (configurable, default 0.90)
    fillRatio := settings.FillRatio
    if fillRatio <= 0 {
        fillRatio = 0.90
    }
    if float64(contextTokens)/float64(contextWindow) < fillRatio {
        return false
    }
    return contextTokens > contextWindow-settings.ReserveTokens
}
```

### 3. Thread model cost through the call chain

The `ShouldCompact` call chain is:

1. `pkg/core/agentsession.go` `checkAutoCompaction()` — has access to `s.Model()` which is `*ai.Model` with `Cost ai.ModelCost` field (`.Input` is $/MTok)
2. `CompactionRunner.ShouldCompact(contextTokens, contextWindow int) bool` — interface in `pkg/core/agentsession.go` line ~80
3. `DefaultRunner.ShouldCompact(...)` in `pkg/core/compaction/runner.go` line ~27

**Changes needed:**

a. Update the `CompactionRunner` interface (`pkg/core/agentsession.go` ~line 80):
```go
ShouldCompact(contextTokens, contextWindow int, inputCostPerMTok float64) bool
```

b. Update `DefaultRunner.ShouldCompact` in `pkg/core/compaction/runner.go`:
```go
func (r *DefaultRunner) ShouldCompact(contextTokens, contextWindow int, inputCostPerMTok float64) bool {
    settings := r.compactionSettings()
    return ShouldCompact(contextTokens, contextWindow, settings, inputCostPerMTok)
}
```

c. Update the call site in `checkAutoCompaction` (`pkg/core/agentsession.go` ~line 750):
```go
shouldCompact := s.compactionRunner.ShouldCompact(contextTokens, contextWindow, model.Cost.Input)
```

### 4. Wire new settings through `compactionSettings()` in the runner

**File:** `pkg/core/compaction/runner.go`, line ~110

The existing `compactionSettings()` builds the internal struct from config. Add the three new fields following the same pattern — `SettingsManager.GetCompactionSettings()` returns pointers, so nil-coalesce to zero values:

```go
func (r *DefaultRunner) compactionSettings() CompactionSettings {
	s := r.SettingsManager.GetCompactionSettings()
	cs := CompactionSettings{
		Enabled:          s.Enabled,
		ReserveTokens:    s.ReserveTokens,
		KeepRecentTokens: s.KeepRecentTokens,
	}
	if s.FillRatio != nil {
		cs.FillRatio = *s.FillRatio
	}
	if s.MaxContextTokens != nil {
		cs.MaxContextTokens = *s.MaxContextTokens
	}
	if s.MaxContextCost != nil {
		cs.MaxContextCost = *s.MaxContextCost
	}
	return cs
}
```

### 5. Update tests

**`pkg/core/compaction/compaction_test.go`** — update existing `ShouldCompact` tests to pass the new `inputCostPerMTok` parameter (use `0` to preserve existing behavior). Add new test cases:

- `FillRatio` at 0.5 triggers compaction earlier than default
- `FillRatio` > 1.0 effectively disables ratio-based compaction
- `MaxContextTokens` triggers compaction when exceeded, even at low fill ratio
- `MaxContextCost` triggers compaction when cost exceeds threshold
- `MaxContextCost` with `inputCostPerMTok=0` (unknown pricing) doesn't trigger
- `MaxContextCost` with very small value (0.001) triggers at low token counts
- Negative values for new fields treated as zero/disabled
- Default behavior unchanged when new fields are zero-valued

**`pkg/core/compaction/runner_test.go`** — update `ShouldCompact` calls if the runner is tested directly.

**`pkg/core/agentsession_test.go`** or `pkg/core/agentsession_compaction_e2e_test.go` — update any mocks of `CompactionRunner` to match the new interface signature. The known mocks are:
- `pkg/core/agentsession_test.go:436` — `mockCompactionRunner.ShouldCompact`
- `pkg/core/agentsession_compaction_e2e_test.go:623` — `rebuildingMockCompactionRunner.ShouldCompact`

Add at least one mock-level assertion that `checkAutoCompaction` passes `model.Cost.Input` correctly to `ShouldCompact`.

### 6. Update CHANGELOG.md

Add under `## [Unreleased]` → `### Added`:
```
- Flexible compaction triggers: configurable `fillRatio`, `maxContextTokens`, and `maxContextCost` settings
```

## Files to modify (summary)

| File | Change |
|------|--------|
| `pkg/core/compaction/compaction.go` | Add fields to `CompactionSettings`, update `ShouldCompact` |
| `pkg/config/settings.go` | Add fields to config `CompactionSettings` |
| `pkg/core/agentsession.go` | Update `CompactionRunner` interface, pass cost in `checkAutoCompaction` |
| `pkg/core/compaction/runner.go` | Update `DefaultRunner.ShouldCompact`, wire new settings |
| `pkg/core/compaction/compaction_test.go` | Update + add tests |
| Any file with a mock `CompactionRunner` | Update interface impl |
| `CHANGELOG.md` | Add entry |

## Verification

Run `make all` — must pass. The key things to check:
1. Zero-valued new fields preserve existing behavior exactly
2. Each new trigger works independently
3. Interface change compiles everywhere (mock implementations too)
