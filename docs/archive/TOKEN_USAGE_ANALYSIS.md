> **Archived.** This analysis has been reviewed and the changes deemed appropriate have been applied. Retained here for historical reference.

# fir Token Usage Analysis
> Analysis of ~/.fir/agent/sessions — 865 sessions, 47,804 assistant turns

## Executive Summary

| Metric | Value |
|--------|-------|
| Total cost | **$2,202** |
| Total tokens | **3.06 billion** |
| Sessions analyzed | 865 |
| Assistant turns | 47,804 |
| Average session cost | $2.54 |
| **Cache hit rate** | **96.3%** ✅ |
| Average turns per session | 55.6 (P90: 152, P99: 439) |

The **cache hit rate is excellent** — prompt caching is working as intended and keeps marginal per-turn costs low. However, several structural patterns are driving unnecessary token consumption. The biggest lever is context growth management.

---

## Token Breakdown

```
Total tokens: 3,055,978,554
  Input (uncached):   108,920,233  (3.6%)   ← paid at full price
  Cache reads:      2,821,027,124  (92.3%)  ← 10× cheaper than input
  Cache writes:       110,178,754  (3.6%)   ← 25% premium on write
  Output:              15,852,443  (0.5%)   ← most expensive per-token
```

Cache reads dominate (92%). Every token in the context window is being cached efficiently; the system re-sends the whole history on each turn but almost all of it hits the cache.

---

## Cost by Model

| Model | Cost | Turns | $/turn | Cache Hit % |
|-------|------|-------|--------|-------------|
| claude-sonnet-4-6 | **$1,090** (50%) | 26,798 | $0.041 | 97.4% |
| claude-opus-4-6 | **$1,033** (47%) | 18,245 | $0.057 | 94.8% |
| claude-haiku-4-5 | $4 | 321 | $0.013 | 91.0% |
| gemini-3-flash | $12 | 455 | $0.026 | 89.5% |
| Others | ~$63 | 2,290 | varies | — |

**Sonnet and Opus together account for 97% of total cost.**  
Opus costs 40% more per turn than Sonnet, and is rarely the right choice for mechanical coding tasks.

---

## Context Growth

Context grows steadily and unboundedly across a session:

| Turn | Avg context (tokens) | Marginal tokens added |
|------|----------------------|----------------------|
| 1 | 5,790 | 5,790 (initial: system prompt + message) |
| 5 | 16,234 | ~3,000/turn |
| 10 | 24,459 | ~1,500/turn |
| 20 | 35,564 | ~800/turn |
| 30 | 44,114 | ~500/turn |
| 50 | ~65,000 | ~400/turn (extrapolated) |
| 100 | ~80,000+ | ~300/turn |

Growth *slows* as sessions mature because old tool results get smaller relative to the total context, but it never stops. At turn 100 a typical session is at 80k+ tokens.

**Sessions with >50k final context (large context burden):** 89 sessions.  
**Sessions with >150k final context:** 21 sessions.

---

## Tool Call Analysis

```
Total tool calls: 51,846

  bash    33,901  (65.4%)   avg result:    946 chars  P90:  2,000  P99: 11,000  Max: 51,278
  read     9,709  (18.7%)   avg result:  4,560 chars  P90: 10,511  P99: 41,186  Max: 51,278
  edit     6,275  (12.1%)   avg result:     72 chars  (confirmation only)
  write    1,029   (2.0%)   avg result:     72 chars  (confirmation only)
  plan       886   (1.7%)   avg result:     76 chars
```

**`bash` is the primary context inflator.** While most bash outputs are short (P50=246 chars), the P99 is 11k chars and the hard cap is 51k chars (50KB). A single large `bash` call can add 10,000+ tokens to the context window.

**`read` is the second largest contributor.** Files average 4.5k chars but the P90 is 10.5k and P99 is 41k. Large files read in full stay in context for the rest of the session.

### Error Rates

| Tool | Error Rate | Total Errors |
|------|-----------|--------------|
| bash | 6.7% | 2,265 |
| edit | 6.4% | 400 |
| read | 0.9% | 85 |

6.7% of bash calls and 6.4% of edit calls fail. Each failed call still adds its error output to the context. Total estimated waste from errors: ~273k tokens (~$0.82).

---

## Compaction Usage

- **Only 52 sessions (5.5%) ever triggered compaction** out of 943 total.
- **133 total compaction events** — most sessions never compact at all.
- The current `maxContextTokens: 800000` means compaction only fires if context exceeds 800k tokens, which almost never happens with Claude's 200k window (the fill-ratio trigger at 90% would fire first at ~180k tokens).
- **`/compact` was invoked manually only 5 times.**
- Compaction summaries are compact (avg 4.5k chars, P90 8.1k chars) — they compress context well when used.

The fill-ratio trigger (90% × context window) does fire in long sessions, but:
1. Many long sessions end before the context window fills up.
2. Sessions with large tool results accumulate context fast but context window never triggers.

---

## Root Cause Analysis

### 1. Context grows linearly without automatic pruning
The current compaction trigger (90% window fill or `maxContextTokens=800k`) is too passive. A 400-turn session accumulates 80-120k tokens worth of accumulated history — most of it stale tool results and old code snippets that the model no longer needs but must still pay to cache-read on every turn.

### 2. Bash output is the largest per-call inflator
65% of tool calls are `bash`. A single `make test` or `git diff` can output 10-50k chars. These large results stay in context for the remaining 50-200 turns of the session, costing cache-reads every turn. The 50KB cap is already a protection, but 50KB (≈12,500 tokens) is still large.

### 3. Files are re-read in full with no deduplication
`read` tool is called 9,709 times with an average result of 4.5k chars. When the same file is read repeatedly across a long session, each read result is a separate entry in the conversation history. A 50k-char file read stays in context as a 12,500-token entry.

### 4. Opus usage is expensive for mechanical work
Claude Opus at $0.057/turn is 40% more expensive than Sonnet ($0.041/turn). 18,245 Opus turns account for $1,033 (47% of total cost). For routine coding/editing work, Sonnet is equally capable at lower cost.

### 5. Short sessions have low cache efficiency
Sessions with 0-9 turns average 51% cache hit rate vs 95%+ for longer sessions. In a fresh session, the first few turns all pay full input price while the cache warms up. High session churn (many short sessions) increases the amortized cost of cache warming.

---

## Recommendations

### 🔴 High Impact — Quick Wins

#### 1. Lower `maxContextTokens` to a proactive threshold (e.g. 80,000–100,000)

**Current:** `maxContextTokens: 800000` — effectively disabled.  
**Recommended:** `maxContextTokens: 80000` (or 100000)

```json
"compaction": {
  "enabled": true,
  "maxContextTokens": 80000,
  "keepRecentTokens": 20000,
  "reserveTokens": 16384
}
```

This triggers compaction once context reaches 80k tokens, replacing the accumulated conversation history with a concise summary (~4-8k chars). The `keepRecentTokens: 20000` ensures the last 20k tokens of recent work are preserved verbatim.

**Estimated impact:** Sessions currently run to 80-200k tokens before ending. Compacting at 80k would cut the average context per turn in half for sessions longer than ~50 turns. Roughly **30-40% reduction in cache write tokens** for long sessions, translating to an estimated **$200-400 in annual savings** at current usage rates.

#### 2. Reduce bash output cap from 50KB to 20KB

**Current:** `DefaultMaxBytes = 50 * 1024` (50KB / ~12,500 tokens)  
**Recommended:** 20KB (~5,000 tokens)

In `pkg/agent/tools/truncate.go`:
```go
DefaultMaxBytes = 20 * 1024  // 20KB — bash outputs rarely need more
```

Most useful bash output (test results, errors, git status) fits in 20KB. The 51KB results in the dataset are almost always git diffs, `make` outputs, or large file reads via `cat` — all cases where the tail is more useful than the full content.

The tool already uses `TruncateTail` for bash (correct: errors appear at the end), so this only affects truly large outputs.

**Estimated impact:** Prevents the worst-case large bash outputs from adding 10-12k tokens to context. For sessions that regularly run `make test` or large diffs, this could reduce per-turn context growth by 20-30%.

#### 3. Use `claude-haiku-4-5` (or another cheap model) for background/fleet agents

Haiku costs $0.013/turn vs Sonnet's $0.041/turn — **3× cheaper**. For shepherd sub-agents running routine tasks (sync, build verification, review passes), switching to Haiku saves ~$0.028/turn. At 18k Opus turns + Sonnet for fleet work, even a 20% shift to Haiku saves ~$100/month.

This can be done per-session with `fir --model haiku` or via `settings.json` overrides in the worktree's `.fir/`.

---

### 🟡 Medium Impact — Implementation Required

#### 4. Add a `--compact-at` flag / per-session maxContextTokens

Currently `maxContextTokens` is global. Different tasks benefit from different thresholds:
- **Sync/research sessions** (read-heavy, stable): can compact at 50k
- **Long development sessions** (iterative): 80-100k is right
- **Review agents** (short, one-shot): no compaction needed

Adding a `--compact-at N` CLI flag allows callers to set the threshold per invocation without changing global settings.

#### 5. De-duplicate tool results in context (within-session read cache)

When the model reads the same file twice within a session (and the file hasn't changed), the second result is a verbatim copy of the first. Both entries stay in context.

**Implementation idea:** In `AgentContext`, track a map of `(tool, canonical_args) → message_index`. When a tool result matches a recent cached result (within the last N turns), replace the result text with a brief reference: `"[same as previous read of <file>]"`.

This would require adding result caching to `executeToolCalls` in `pkg/agent/loop.go`. The cache key for `read` would be `(path, offset)`. For `bash` it's harder (non-deterministic), but could be applied to read-only commands.

**Estimated impact:** The `read` tool is called 9,709 times. If even 20% of reads are re-reads of unchanged files, this saves ~400k token·sessions worth of context.

#### 6. Reduce read file truncation limit for large files

**Current:** Read is truncated at 50KB / 2000 lines (same as bash).  
**Problem:** P99 read result is 41k chars — these are near-max reads.

For large source files, the model rarely needs all 2000 lines. More often it needs:
- A specific function (use `bash grep -n` to find it first, then `read --offset`)
- The first 50-100 lines (imports/header)
- The last 50-100 lines (recent changes)

**Recommendation:** Lower the read limit to 500 lines / 20KB as the default, with an `--limit` option exposed in the tool args for when the model explicitly needs more. The `read` tool already supports `offset` and `limit` parameters — the model just defaults to reading the whole file.

Or: expose `DefaultMaxLines=200` for read when context is already large (>50k tokens), scaling down the limit dynamically.

#### 7. Add per-turn context size feedback in the footer

The TUI footer currently shows thinking level and model but not context size. Adding a token counter (sourced from the last assistant turn's `usage.input + usage.cacheRead`) would make context bloat visible during long sessions, prompting the user to `/compact` manually before the context grows too large.

This is a display-only change in `pkg/modes/interactive/components/footer.go`.

---

### 🟢 Low Impact — Good Hygiene

#### 8. Automatically `/compact` when switching between major tasks

When the user types a new unrelated query mid-session, fir could detect the topic shift (via a small heuristic: new query doesn't reference any files/functions from the last 10 turns) and suggest compaction. This would prevent context from the previous task bleeding into and inflating cost for the new task.

#### 9. Clean up test session residue

There are 20+ session directories from `TestACP_E2E_*` integration tests in `~/.fir/agent/sessions/`. These don't affect cost (they're test artifacts) but clutter the session directory. Add a `--cleanup-test-sessions` flag or periodic cleanup in `make clean`.

#### 10. Consider `--no-skills` for non-fir project sessions

The skills section in the system prompt adds ~1,725 tokens per session (6,902 chars / 4). For sessions on other projects that don't use fir's built-in skills (tau, claude-tray, etc.), passing `--no-skills` would save the cache-write cost of the skills block on every fresh session.

For the fir project itself, skills are valuable and should stay.

---

## Quick Reference: Settings to Change Today

```json
// ~/.fir/agent/settings.json
{
  "compaction": {
    "enabled": true,
    "maxContextTokens": 80000,
    "keepRecentTokens": 20000,
    "reserveTokens": 16384
  }
}
```

And in `pkg/agent/tools/truncate.go`:
```go
DefaultMaxBytes = 20 * 1024  // was 50 * 1024
```

These two changes require minimal code and address the highest-impact issues.

---

## Appendix: Full Data Tables

### Top 10 Most Expensive Sessions

| Session | Total tokens | Turns | Cache % | Final ctx |
|---------|-------------|-------|---------|-----------|
| 2026-02-24 (unnamed) | 123,731,807 | 1,306 | 97% | 105,344 |
| 2026-02-27 (unnamed) | 87,807,954 | 804 | 100% | 136,664 |
| 2026-02-27 (unnamed) | 54,995,531 | 647 | 99% | 65,441 |
| reorg | 45,872,512 | 546 | 99% | 160,615 |
| 2026-02-27 (unnamed) | 41,384,731 | 504 | 99% | 85,086 |
| 2026-02-19 (unnamed) | 41,097,369 | 506 | 100% | 136,077 |
| 2026-02-21 (unnamed) | 37,430,118 | 307 | 98% | 61,879 |
| fir-ext-review-and-fix | 34,704,837 | 302 | 98% | 190,373 |
| 2026-02-27 (unnamed) | 33,505,785 | 392 | 94% | 174,723 |
| anthropic-api-tools | 32,648,287 | 388 | 95% | 74,505 |

The top session alone consumed 123M tokens across 1,306 turns. This is a 3.5-day marathon session. With proactive compaction at 80k tokens, this session would have compacted ~10 times, saving an estimated 40-60% of its total token cost.

### Compaction Settings Reference

| Setting | Current | Recommended | Effect |
|---------|---------|-------------|--------|
| `enabled` | true | true | No change |
| `maxContextTokens` | 800,000 | **80,000** | Compact at 80k instead of 800k |
| `keepRecentTokens` | 20,000 | 20,000 | Keep last 20k tokens verbatim |
| `reserveTokens` | 16,384 | 16,384 | Reserve for summary generation |
