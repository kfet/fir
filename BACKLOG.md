# Backlog

Tracked follow-ups for fir. Items here are non-urgent but should not be lost.

## Coverage gate: shrink the `.covignore` ledger

fir adopted the sibling-repo `covgate` gate (`make coverage`, wired into
`make all`) at **67.0% whole-tree coverage**. A `-min=100` gate cannot be
honest at that starting point, so the root `.covignore` carries an explicit
exclusion ledger. This entry is the debt record; the goal is to delete lines
from that file until only the structural section remains.

**Where the numbers stand at adoption** (31,495 statements total):

| Bucket | Statements | Share |
| --- | ---: | ---: |
| Gated at 100% | 372 | 1.2% |
| Excluded — structural | 12,446 | 39.5% |
| Excluded — pure debt | 18,677 | 59.3% |

The gated scope is `pkg/ai` (hand-written), `pkg/envvars`,
`pkg/mcp/history` and `pkg/modes/print`. All four were taken to exactly 100%
as part of the adoption, so the gate is small but real — it is not measuring
a generated table.

**Why a tiny scope is still worth having.** The gate is an *invariant*, not
a trend: one uncovered statement in a gated package fails the build. And the
`.covignore` patterns are deliberately non-recursive, so a **brand-new
package is gated at 100%** until someone consciously adds it to the ledger.
That is where the gate has teeth today — on new code. The second tier (whole
tree, no ignore file, `COVERAGE_FLOOR` in the Makefile, currently 66) exists
only to catch catastrophic rot in the 98.8% tier 1 cannot see; ratchet it up
when headroom appears, and re-base it downward only in a commit that says so
and why (landing a large, legitimately-excluded feature dilutes the
whole-tree number through no sin). Tier 1's 100% never moves.

**Section 1 — structural (defensible long-term).** Process entrypoints
(`cmd/fir`), the build-time model generator (`cmd/generate-models`), the
Bubble Tea TUI and widget rendering (`pkg/modes/interactive*`,
`pkg/tui/components`), self-update, session re-exec, the OS clipboard shim,
the standalone binary-size skill helper, and the generated
`pkg/ai/models_generated.go`. Covering these means testing the terminal, the
OS or the network rather than fir's logic. These lines may legitimately
never leave the file — though the right long-term move for several is to
push the unmockable part behind a narrow wrapper and shrink the exclusion to
that wrapper, per covgate's `ext.go` convention. The weakest member is
`pkg/modes/interactive/commands.go` (~1,100 statements of slash-command
dispatch): it is structural only because it is welded to the TUI model, and
splitting the dispatch table out would move most of it into scope.

**Section 2 — pure debt (no structural excuse).** Ordinary, testable Go that
simply is not covered yet. Promotion queue, cheapest first (uncovered
statements as of adoption):

| # | Package | Uncovered | Total |
| --- | --- | ---: | ---: |
| 1 | `pkg/extension/apikind` | 6 | 6 |
| 2 | `pkg/ai/envkeys` | 18 | 47 |
| 3 | `pkg/extension/sdk` | 21 | 64 |
| 4 | `pkg/ai/providers/declcfg` | 21 | 122 |
| 5 | `pkg/agent/tools` | 26 | 117 |
| 6 | `pkg/log` | 44 | 199 |
| 7 | `pkg/mcp/autoreply` | 72 | 253 |
| 8 | `pkg/session/compaction` | 77 | 600 |
| 9 | `pkg/auth` | 134 | 525 |
| 10 | `pkg/session/store` | 147 | 1015 |
| 11 | `pkg/config` | 158 | 593 |
| 12 | `pkg/pkg` | 161 | 520 |
| 13 | `pkg/resources` | 198 | 922 |
| 14 | `pkg/mcp` | 238 | 1602 |
| 15 | `pkg/models` | 266 | 1317 |
| 16 | `pkg/session` | 354 | 1200 |
| 17 | `pkg/modes/acp` | 769 | 2340 |
| 18 | `pkg/extension` | 877 | 2645 |
| 19 | `pkg/ai/providers` | 1476 | 4590 |

The first six are sub-day units and are the on-ramp: without one, a ledger
of 2,000-statement packages never moves. Clearing section 2 entirely would
put the gate over 19,049 statements — **60% of the tree** — at a hard 100%.

**Measuring progress.** `make coverage` prints both numbers under `V=1`.
Three things should move monotonically: rows deleted from section 2 of
`.covignore`, the gated statement count (372 at adoption), and
`COVERAGE_FLOOR`. If none of them has moved in a release cycle, the ledger
has become a carve-out and this entry has failed.

## ✅ DONE — Generic passthrough content variant for server-side blocks (v0.46.4)

Landed in v0.46.4. Server-side blocks (`server_tool_use`,
`web_search_tool_result`, `code_execution_tool_result`,
`web_fetch_tool_result`, `tool_invocation`, `tool_output`) now round-trip
verbatim via the new `ai.ServerContent` variant (stores `ProviderType`
+ raw JSON bytes + display-formatted text). The Anthropic streamer
captures the original `content_block` JSON as `Raw`, formats display
text once at stream time and stores it as `Display`, and
`convertAnthropicMessages` emits `Raw` back on the wire so that
signed thinking blocks that originally sandwiched a server block keep
their structural separator.

Cross-provider replay (`TransformMessages`) drops server blocks when
crossing providers — they're provider-specific and would 400 against
OpenAI/Google — and downgrades the `Display` text to a plain `text`
block so user intent survives.

The v0.46.3 band-aids (`separateAdjacentThinkingBlocks` wire-time
guard, non-empty `[server tool: <name>]` placeholder) are KEPT as
defence-in-depth for sessions whose history was stored under the
older text-flattened format. Once such sessions age out, both can be
removed.

**Possible removal of band-aids** (eventually):
- Drop the `case "server_tool_use", "web_search_tool_result", …` text
  placeholder fallback in `pkg/ai/providers/anthropic.go` once we're
  confident no stored history contains them.
- Drop `separateAdjacentThinkingBlocks` and its call site once the
  same condition holds.
- Keep `anthropic_adjacent_thinking_test.go` / the splice case in
  `anthropic_thinking_invariants_test.go` retired as a historical
  marker, or rewrite to assert the new property "no adjacent
  thinking blocks ever leave fir on the wire" without involving the
  splice.

**Reporting upstream.** Anthropic's API exhibits an inconsistent
contract: the streaming response emits assistant content shapes that
its own input validator will then reject on replay (consecutive
`thinking` blocks). Worth filing now that the proper fix is in and we
can credibly say "this is your bug, not ours".

## Deferred: self-improving-agents review (2026-08-03)

Reviewing arXiv:2607.13104 against fir produced one real fix — the autoresearch
benchmark lock, shipped in `eab9a1a2` + `bbb93991` — and three items deliberately
NOT built. Each is recorded with the trigger that would change the verdict; absent
that trigger, building it is theatre.

**Memory hygiene (the missing CRUD "D").** fir's durable state is append-only:
`doctor.jsonl`, `instruction-feedback.jsonl`, agent notes. No consolidation, no
expiry. Judged out of scope — `instruction-tune` already archives, and note bloat
belongs to whatever keeps the notes, not to fir. *Trigger: fir grows a first-class
agent-memory mechanism of its own; then it needs a delete/consolidate story from
day one.*

**Per-skill regression evals for `instruction-tune`.** An eval harness so skill
edits are verifier-gated rather than reviewed by eye. Judged unnecessary:
`instruction-tune` proposes a diff and applies only on user consent, so the
acceptor already exists and is a human. *Trigger: `instruction-tune` (or anything
else) starts editing `AGENTS.md`/skills without a human in the loop.*

**Shared versioned artifact pool for fleets.** A cross-agent repository of reusable
artifacts (benchmarks, tool wrappers, skill patches). Judged redundant: git
branches plus `fir-exts` packages already serve it. *Trigger: fleets are observed
independently re-deriving the same artifacts, i.e. the duplication is real rather
than anticipated.*
