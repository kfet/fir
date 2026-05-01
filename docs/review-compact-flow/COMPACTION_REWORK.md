# Compaction rework — design + tracker

Status: **Phases 0–2 complete on branch `review-compact-flow`.** Phases 3–4 pending (require eval harness).
Branch: `review-compact-flow`
Owner: TBD

This doc captures the review findings, the chosen direction, and the staged
work plan for reworking fir's session compaction. Use it as a handoff —
everything needed to pick this up cold should be here.

---

## TL;DR

fir's compaction is structurally sound (structured Markdown summaries with
explicit file tracking — Factory.ai's benchmark shows this design wins) but
leaks information that a coding agent needs to keep working: tool outputs
are lossy-summarized, file-op tracking misses bash writes, and the trigger
fires too late. The package also has architectural drift — it lives outside
`pkg/session` and reaches into the store + agent through a fig-leaf
interface.

The plan: move compaction under `pkg/session`, introduce a neutral
`Artifact` type to plug the leak, then ship pointer-stub-based observation
masking (re-using session-store entry IDs) before investing in
eval-gated changes (cheaper compaction model, ACON-style prompt
optimization, live-context masking).

Phases 0–2 (~3.5 days) capture the largest research-backed win without
needing measurement infrastructure. Phase 3+ requires an eval harness.

---

## Background — how `/compact` works today

### Entry points

- **Slash command** — `pkg/resources/slashcmds.go:51`, dispatched in
  `pkg/modes/interactive/commands.go:167` → `handleCompactCommand` →
  `executeCompaction`. Optional args become `customInstructions`.
- **Auto-compaction** — `pkg/session/agentsession.go` checks on `agent_end`
  and on context-overflow errors. `compactionRunner.ShouldCompact` decides;
  `runAutoCompaction` emits `auto_compaction_start` / `_end` events.
- **ACP mode** — same entrypoint via `pkg/modes/acp/commands.go`.

### Pipeline (manual `/compact`)

1. **Guard** (`commands.go:240`): refuses if fewer than 2 messages.
2. **Cancel context** + **streaming progress callback** wired via
   `session.WithCompactionProgress(ctx, fn)`. ESC cancels the LLM stream.
3. **`session.RunCompaction`** delegates to `compaction.DefaultRunner.RunCompaction`
   (`pkg/compaction/runner.go`).
4. **`PrepareCompaction`** (`compaction.go:476`):
   - Returns nil if last entry is already `compaction` ("already compacted").
   - Finds previous compaction → `boundaryStart = prev+1`.
   - Estimates `tokensBefore` from messages since prev compaction (last
     assistant `Usage` if available, else chars/4 fallback).
   - **`FindCutPoint`**: walks backwards summing `EstimateTokens` until ≥
     `KeepRecentTokens` (default 20k), snaps to a valid cut point. If cut
     lands mid-turn it computes `TurnStartIndex` and marks `IsSplitTurn`.
   - Splits into `MessagesToSummarize` (history before cut) and optional
     `TurnPrefixMessages` (prefix of split turn).
   - Extracts read/edited file lists; carries forward from previous
     compaction's `Details`.
5. **`Compact`** (`compaction.go:790`):
   - Streams summary via `ai.StreamSimple` with `MaxTokens = 0.8 *
     ReserveTokens` and **`Reasoning: ThinkingHigh`**.
   - Prompt: `summarizationPromptText` initially, or
     `updateSummarizationPromptText` when there's a `PreviousSummary`.
     Custom instructions append as "Additional focus: …".
   - Split-turn case: second turn-prefix summary (`MaxTokens = 0.5 *
     ReserveTokens`, no high reasoning), concatenated with `---\n\n**Turn
     Context (split turn):**`.
   - Appends `FormatFileOperations(readFiles, modifiedFiles)`.
6. **Persist** — `sess.SessionStore.AppendCompaction(...)`. Then
   `BuildSessionContext` reconstructs message list, `Agent.ReplaceMessages`
   swaps in-memory state.
7. **UI rebuild** (`commands.go:307`). If `HasPendingWork()`, spawns
   "Inferring..." loader and calls `Agent.Continue()`.

### Auto-trigger thresholds

`ShouldCompact` (`compaction.go:175`):

- Hard cap: `MaxContextTokens > 0 && contextTokens > MaxContextTokens`.
- Soft: `fillRatio ≥ 0.90` AND `contextTokens > contextWindow - ReserveTokens`
  (default `ReserveTokens=16384`, `KeepRecentTokens=20000`).

### Replay

`BuildSessionContextFromEntries` (`session.go:1033`): the compaction entry
becomes a synthetic `CompactionSummaryMessage` injected first; only entries
from `FirstKeptEntryID` onward in the path follow. Earlier history is
dropped from LLM context but **persists on disk**.

### Current compaction model

Same as the session model. `pkg/compaction/runner.go:49` does
`model := sess.Model()`. Main summary uses `Reasoning: ThinkingHigh`
(`compaction.go:694`); turn-prefix summary does not. No separate
compaction-model setting exists.

---

## Review — is the resulting context usable?

### What works

1. **Cut-point logic is conservative and correct.** Snaps to user/turn
   boundaries, handles split turns with a separate turn-prefix summary.
2. **File ops tracked structurally** via `<read-files>` / `<modified-files>`
   XML, deduped, **carried forward across compactions** via `Details`. This
   is the single most important structured signal.
3. **`updateSummarizationPromptText`** preserves prior summary, merges new
   info, moves "In Progress" → "Done". Without this, multi-compaction
   sessions would drift catastrophically.
4. **Anti-roleplay guardrails** in the system prompt are present.

### Where it leaks usable context

1. **Tool-result text is dumped verbatim into serialization, then
   summarized away.** A 50KB grep output and a 200-byte `ls` look identical
   to the summarizer. Exact diffs, error messages, test output, search
   results — all paraphrased or dropped.
2. **Only `read`/`write`/`edit` count as file ops.** Misses `bash` (cat,
   sed, awk, heredoc-write, redirects), `MultiEdit`, custom MCP file tools,
   shell redirection. `<modified-files>` understates reality — a
   correctness issue, not just fidelity.
3. **No structured "current code state" anchor.** No working set, no
   pending invariants, no open tool calls as discrete items.
4. **`KeepRecentTokens=20000` is the only thing that reliably survives.**
   ~1–2 turns on a real coding session. Older user constraints rely on
   prose distillation.
5. **`EstimateTokens` is chars/4** — `KeepRecentTokens` is enforced against
   this estimate, not provider count. Heavy-tool sessions go sideways.
6. **`ThinkingHigh` only on main summary**, not turn-prefix — inconsistent.
7. **Thinking blocks serialized into summarizer input** — bloats input,
   biases summary toward agent's prior CoT, leaks reasoning across
   compactions.
8. **No verification pass.** Summary taken as-is.
9. **Custom instructions land as a footer** — under-weighted by the model
   vs the rigid format spec above.
10. **"Done" list grows without bound** across compactions; no prune rule.

### Bottom line

For talk-mostly sessions: fine. For heavy coding sessions: the resulting
context is plausibly continuous but factually thin. Agent knows *what* it
was doing, only roughly *where it got to*, almost nothing about *what the
environment actually looks like now*. The 20k recent-tail carries the load;
the summary is more orientation than working memory.

---

## Research findings (late 2025 / early 2026)

Sources: Factory.ai context-compression benchmark, JetBrains "Cutting
Through the Noise" (2025-12), Morph compaction-vs-summarization writeup,
ACON paper (arxiv 2510.00615), Microsoft Agent Framework docs, Zylos
context-rot research.

1. **Probe-based eval, not ROUGE.** Factory built recall / artifact /
   continuation / decision probes over 36,611 messages. Lexical overlap
   tells you nothing about whether an agent can continue.
2. **Structured summaries beat freeform AND opaque compression.** Quality
   scores: Factory 3.70, Anthropic 3.44, OpenAI 3.35 — all >98% compression.
   Section structure forces preservation.
3. **Artifact tracking is the universal weak spot.** All methods 2.19–2.45/5.0
   on file-modification recall. fir's `<modified-files>` design is exactly
   the pattern that wins this metric — extend it.
4. **Verbatim deletion (Morph) preserves technical detail** that
   summarization paraphrases away. Hybrid: verbatim-keep paths + errors,
   summarize prose.
5. **Observation masking (JetBrains) — strongest practical result.**
   Mask stale tool outputs while keeping the action and reasoning visible.
   On SWE-bench, matches full LLM summarization quality at less compute.
6. **Trigger at 70%, not 90%.** Performance degrades past ~30k tokens
   regardless of context-window size. 65% of enterprise AI failures in 2025
   attributed to drift not exhaustion.
7. **ACON: failure-driven prompt optimization.** Diff success-with-full vs
   success-after-compression, mutate the compression prompt. 26–54% peak
   token reduction, gradient-free.
8. **Use a cheaper model for compaction.** Cursor and Microsoft Agent
   Framework both recommend flash/mini tier.
9. **Atomic message groups, not arbitrary cut points.** Tool-call ↔
   tool-result must move together — fir handles this but bespokely.
10. **Summarization drift is real.** Mitigation: keep raw memories linked
    to summaries. fir's session log already preserves raw history — just
    not queryable.

---

## Architectural decisions

### Decision 1: move `pkg/compaction` → `pkg/session/compaction`

Compaction is a session operation. The `CompactionRunner` interface already
lives in `pkg/session/agentsession.go`; only the implementation is exiled.
`pkg/compaction` already imports `pkg/session` + `pkg/session/store`; the
dependency only points one way. Three call sites construct `DefaultRunner`
(`cmd/fir/app.go`, `cmd/fir/login.go`, `pkg/modes/acp/acp.go`).

Layout after the move:

```
pkg/session/
  agentsession.go          # CompactionRunner interface (stays)
  artifact.go              # NEW: neutral Artifact type + accessors
  compaction/
    compaction.go          # cut-point + orchestration (moved + slimmed)
    cutpoint.go            # split out
    prompts.go             # split out
    tokens.go              # split out
    fileops.go             # moved
    serialize.go           # moved
    runner.go              # DefaultRunner (config/models deps live here)
```

`pkg/session` itself stays free of `config` + `models` imports. Wiring deps
remain only in `compaction/runner.go`.

### Decision 2: neutral `Artifact` type at session boundary

Today `pkg/compaction` reaches into `store.SessionEntry`,
`store.CreateCompactionSummaryMessage`, `sess.SessionStore.GetBranch()`,
`sess.SessionStore.AppendCompaction()`, `sess.Agent.ReplaceMessages()`.
The `CompactionRunner` interface gives a fig-leaf abstraction; the runner
unwraps it.

Introduce `pkg/session/artifact.go`:

```go
type ArtifactKind int
const (
    ArtifactUser ArtifactKind = iota
    ArtifactAssistant
    ArtifactToolResult
    ArtifactBranchSummary
    ArtifactCompactionSummary
    ArtifactCustom
)

type Artifact struct {
    EntryID  string             // stable session-store ID — used as stub key
    Kind     ArtifactKind
    Message  agent.AgentMessage // already neutral
    Bytes    int                // for masking decisions
    ToolName string             // for ToolResult; empty otherwise
}
```

Narrow API on `AgentSession`:

```go
// Inputs to compaction (replaces direct GetBranch + getMessageFromEntry)
func (s *AgentSession) CompactionArtifacts() (
    artifacts []Artifact,
    prevSummary string,
    prevCompactionIdx int,
)

// Output of compaction (replaces direct AppendCompaction + ReplaceMessages)
type CompactionOutput struct {
    Summary           string
    FirstKeptEntryID  string
    TokensBefore      int
    ReadFiles         []string
    ModifiedFiles     []string
}
func (s *AgentSession) ApplyCompaction(out CompactionOutput) error
```

After this:

- `pkg/session/compaction` imports only `pkg/session`, `pkg/agent`,
  `pkg/ai` — no `store`, no `config`, no `models` (except in `runner.go`).
- Store schema can change without touching compaction logic.
- Pointer-stubs become natural: `Artifact.EntryID` is the stub key.
  `SerializeConversation` emits `[entry e7f3a2 ...]` directly.
- File-op `entry_id` association falls out trivially.

The half-formed `compaction.SessionEntry` struct (`compaction.go:23`,
currently unused for traversal) gets deleted; `Artifact` replaces it.

### Decision 3: pointer-stubs via session-store entry IDs, no new tools

The session store **is already content-addressable**. Every entry has a
stable `ID`. We're just not using it as such.

In `SerializeConversation`, when a tool result is older than N turns or
larger than M bytes, replace its full text with a stub:

```
[entry e7f3a2 tool=bash bytes=48213 head="make: *** [test] Error 1" ...tail]
```

No new `recall` tool. Add a 2-line instruction to the system + summary
prompt: *"Older tool outputs are elided as `[entry <id> ...]`. To recover,
re-run the command or re-`read` the file. The session store retains them
but isn't directly queryable."*

For files: re-reading is correct anyway (file may have changed).
For commands: re-running gives current state, usually what you want.
For irreproducible content: head/tail snippet has to suffice — and would
have been lost under summarization too.

### Decision 4: don't break prompt caching

Pointer-stubs initially apply to **summarizer input only** (Phase 2). Live
LLM context stays byte-identical. Anthropic/OpenAI prompt-cache prefixes
remain valid. Only Phase 4's "live-context observation masking" risks cache
invalidation; gate that on the eval harness.

---

## Recommendation table (canonical)

| # | Change | Effort | Risk | Depends on |
|---|---|---|---|---|
| 1 | **Move `pkg/compaction` → `pkg/session/compaction`**, split `compaction.go` (850 lines) into `compaction.go` / `cutpoint.go` / `prompts.go` / `tokens.go` while moving | 0.5d | Low | — |
| 2 | **Introduce `session.Artifact` + `CompactionArtifacts()` / `ApplyCompaction()` API**, drop direct `store` + `Agent` access from compaction | 0.5d | Low | 1 |
| 3 | **Add `EntryID` to file-op tracking**, render as `path (entry e7f3a2)` in `<modified-files>` | 1h | None | 2 |
| 4 | **Stub old/large tool results in `SerializeConversation`** as `[entry <id> tool=<name> bytes=<n> head="..." tail="..."]` (summarizer input only, not live context) | 0.5d | Low | 2 |
| 5 | **Add 2-line "recall" instruction** to summary + system prompt: re-run command or re-`read` file. No new tools | Trivial | None | 4 |
| 6 | **Drop `[Assistant thinking]:` from summarizer input** | Trivial | None | — |
| 7 | **Lower trigger from 90% → 70% fill ratio**, drop AND-with-reserve gate | Trivial | Low | — |
| 8 | **Extend file-op extraction to bash + MCP tools** (redirects, `sed -i`, `tee`, MultiEdit) | Low | Low | 3 |
| 9 | **Bound the "Done" list** in update prompt to last N items | Trivial | None | — |
| 10 | **Promote `/compact <instructions>` to first-class section**, not trailing footer | Low | None | — |
| 11 | **Add structured "Working Set" section** (files + one-line status) to summary format | Low | None | 8 |
| 12 | **Verbatim "Facts" section**: regex-extract file paths, errors, exit codes, command lines into a preserved-as-is block | Medium | Low | — |
| 13 | **Throttle TUI re-renders** during streaming progress | Trivial | None | — |
| 14 | **Build probe-based eval harness** (recall / artifact / continuation / decision over saved sessions) | Medium | None | 2 |
| 15 | **Switch compaction model to a cheap flash/mini tier**, drop `ThinkingHigh` | Low | Low | 14 |
| 16 | **Verification pass**: post-summary "next action" check, fall back to keeping more raw history if empty | Low | Low | 14 |
| 17 | **Replace chars/4 cut estimation** with provider tokenizer or per-provider calibration | Medium | Low | — |
| 18 | **Observation masking in live LLM context** (not just summarizer input) at compaction boundary — JetBrains-grade win, riskier (tool_use pairing, prompt-cache invalidation) | 1d | Medium | 4, 14 |
| 19 | **ACON loop** via fir's autoresearch skill — auto-mutate summarization prompt from failure diffs. 26–54% peak token reduction reported | Medium-high | Low | 14 |

---

## Phased plan & status

### Phase 0 — structural (1 day)

Goal: eliminate architectural drift, plug the boundary leak.

- [x] **#1** Move `pkg/compaction` → `pkg/session/compaction`. Split
      `compaction.go` into `compaction.go` / `cutpoint.go` / `prompts.go` /
      `tokens.go`. Update three call sites (`cmd/fir/app.go`,
      `cmd/fir/login.go`, `pkg/modes/acp/acp.go`).
- [x] **#2** Introduce `pkg/session/artifact.go` with `Artifact` type +
      `CompactionArtifacts()` / `ApplyCompaction()` accessors. Refactor
      compaction to use them. Delete the unused `compaction.SessionEntry`
      struct.

### Phase 1 — free wins (1 day)

No measurement needed. Pure improvements.

- [x] **#3** Add `EntryID` to file-op tracking.
- [x] **#5** Add recall instruction to system + summary prompt.
- [x] **#6** Drop `[Assistant thinking]:` from summarizer input.
- [x] **#7** Lower trigger to 70%.
- [x] **#9** Bound "Done" list.
- [x] **#10** Promote `/compact <instructions>` to first-class section.
- [x] **#13** Throttle TUI re-renders.

### Phase 2 — pointer-stubs + artifact tracking (1.5 days)

The bulk of the "artifacts as pointers" idea. Touches summarizer input
only — live LLM context unchanged, no cache risk.

- [x] **#4** Stub old/large tool results in `SerializeConversation`.
- [x] **#8** Extend file-op extraction to bash + MCP tools.
- [x] **#11** Add "Working Set" section.
- [x] **#12** Verbatim "Facts" section.

### Phase 3 — eval-gated (3+ days)

- [ ] **#14** Probe-based eval harness over saved sessions.
- [ ] **#15** Switch compaction model to flash/mini tier (gated on #14).
- [ ] **#16** Verification pass (gated on #14).
- [ ] **#17** Provider tokenizer for cut-point estimation.

### Phase 4 — advanced (open-ended)

- [ ] **#18** Live-context observation masking at compaction boundary.
- [ ] **#19** ACON loop via autoresearch.

---

## Open questions

1. **Stub head/tail size.** Proposed 256 chars total. Tune based on
   eval harness — too small loses error messages, too large defeats the
   purpose.
2. **Stub trigger threshold.** When does a tool result get stubbed? By age
   (older than turn N from cut point) or by size (> M bytes) or both?
   Suggest both, OR'd: "older than 3 turns OR larger than 4KB".
3. **`session.Model()` + `ThinkingHigh` is wasteful, but**: is the structured
   summary actually within reach of a flash model? Eval needed.
4. **Live-context masking and prompt caching.** Anthropic caches by exact
   prefix bytes. Stubs would invalidate every cache breakpoint older than
   the masking boundary. Either (a) only mask at compaction (rewriting the
   prefix once anyway), or (b) accept the cache cost. Need numbers.
5. **`recall` via re-running commands** is fine for idempotent ops. What
   about non-idempotent ones (network calls, state mutations)? Currently
   the fallback is "the head/tail snippet must suffice" — measure how often
   this bites.

---

## Pointers (for the next agent)

- Entry: `pkg/compaction/runner.go` — start here, this is the orchestration.
- Cut points: `pkg/compaction/compaction.go` `FindCutPoint`, `PrepareCompaction`.
- Prompts: `pkg/compaction/compaction.go` lines ~552–660.
- Serialization: `pkg/compaction/serialize.go`.
- File ops: `pkg/compaction/fileops.go` — `ExtractFileOpsFromMessage` is
  where bash extraction needs to land.
- Session store: `pkg/session/store/session.go` `BuildSessionContextFromEntries`
  (reconstructs LLM context after compaction); `AppendCompaction`.
- Auto-trigger: `pkg/session/agentsession.go` lines ~733–870.
- TUI: `pkg/modes/interactive/commands.go` `executeCompaction` (~line 250).

---

## Changelog

- _2025-05-01_ — initial doc, no work started.
- _2026-05-01_ — Phases 0–2 implemented on branch `review-compact-flow`. 13 of 19 recommendations shipped (#1, #2, #3, #4, #5, #6, #7, #8 partial, #9, #10, #11, #12, #13). Outstanding: #8 sed -i tokeniser, #14–#19 pending eval harness.
