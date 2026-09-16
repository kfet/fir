# Backlog

Tracked follow-ups for fir. Items here are non-urgent but should not be lost.

## Coverage gate: shrink the `.covignore` ledger

fir adopted the sibling-repo `covgate` gate (`make coverage`, wired into
`make all`) at **67.0% whole-tree coverage**. A `-min=100` gate cannot be
honest at that starting point, so the root `.covignore` carries an explicit
exclusion ledger. This entry is the debt record; the goal is to delete lines
from that file until only the structural section remains.

**Where the numbers stand at adoption** (31,495 statements total):

| Bucket | At adoption | Today | Share today |
| --- | ---: | ---: | ---: |
| Gated at 100% | 372 | 1,483 | 4.5% |
| Excluded — structural | 12,446 | 13,078 | 40.1% |
| Excluded — pure debt | 18,677 | 18,033 | 55.3% |

The tree has grown from 31,495 to 32,594 statements since adoption, which is
why the structural bucket rose without a line being added to section 1.

The gated scope at adoption was `pkg/ai` (hand-written), `pkg/envvars`,
`pkg/mcp/history` and `pkg/modes/print`. All four were taken to exactly 100%
as part of the adoption, so the gate is small but real — it is not measuring
a generated table.

**Sealed since adoption.** Seven packages have been promoted out of section
2 at 100%, taking the gated scope from 372 to **1,483 statements** (4.5% of
the tree) and the whole-tree number from 67.8% to 68.7%:

| Package | Statements | Queue item |
| --- | ---: | --- |
| `pkg/extension/apikind` | 6 | 1 |
| `pkg/ai/envkeys` | 47 | 2 |
| `pkg/extension/sdk` | 66 | 3 |
| `pkg/ai/providers/declcfg` | 121 | 4 |
| `pkg/agent/tools` | 117 | 5 |
| `pkg/log` | 199 | 6 |
| `pkg/pkg` | 518 | 12 (out of order) |

Nothing was added to the ledger to achieve this: every uncovered branch was
either reached by a test or deleted as genuinely dead code. Filesystem and
git work is done against `t.TempDir()`; no test touches the network. Two
real bugs were found and fixed on the way, one more was documented but not
fixed, and one whole untested failure surface was closed (see CHANGELOG
under `## [Unreleased]`).

Note that three promoted packages force failures with `chmod 0500`, which is
a no-op under root — the suite must be run unprivileged, and now that these
packages are gated at 100% that is enforced rather than merely advisable.

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
| ~~1~~ | ~~`pkg/extension/apikind`~~ | — | 6 (sealed) |
| ~~2~~ | ~~`pkg/ai/envkeys`~~ | — | 47 (sealed) |
| ~~3~~ | ~~`pkg/extension/sdk`~~ | — | 66 (sealed) |
| ~~4~~ | ~~`pkg/ai/providers/declcfg`~~ | — | 121 (sealed) |
| ~~5~~ | ~~`pkg/agent/tools`~~ | — | 117 (sealed) |
| ~~6~~ | ~~`pkg/log`~~ | — | 199 (sealed) |
| 7 | `pkg/mcp/autoreply` | 72 | 253 |
| 8 | `pkg/session/compaction` | 77 | 600 |
| 9 | `pkg/auth` | 134 | 525 |
| 10 | `pkg/session/store` | 147 | 1015 |
| 11 | `pkg/config` | 158 | 593 |
| 12 | ~~`pkg/pkg`~~ | ~~161~~ | ~~520~~ → 518 (sealed) |
| 13 | `pkg/resources` | 198 | 922 |
| 14 | `pkg/mcp` | 238 | 1602 |
| 15 | `pkg/models` | 266 | 1317 |
| 16 | `pkg/session` | 354 | 1200 |
| 17 | `pkg/modes/acp` | 769 | 2340 |
| 18 | `pkg/extension` | 877 | 2645 |
| 19 | `pkg/ai/providers` | 1476 | 4590 |

The first six were sub-day units and were the on-ramp: without one, a ledger
of 2,000-statement packages never moves. **All six are now sealed**, along
with `pkg/pkg` (item 12, taken out of order). Clearing the rest of section 2
would put the gate over 19,000 statements — **60% of the tree** — at a hard
100%. `pkg/mcp/autoreply` is the next cheapest.

**Measuring progress.** `make coverage` prints both numbers under `V=1`.
Three things should move monotonically: rows deleted from section 2 of
`.covignore`, the gated statement count (372 at adoption, 1,483 today), and
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

## Deferred: immutable releases on kfet/fir-dist (2026-09-01)

**Enable GitHub immutable releases on the distribution mirror.** fir-dist is a
binary-distribution endpoint that every installed fir self-updates from, so
asset immutability is the right posture there — it complements the sha256 the
Homebrew formula already pins, and closes silent asset replacement under a
published tag.

Not done today because it is a mirror-step rewrite, not a flag flip: immutable
releases lock assets after publication, and `release.yml` currently re-uploads
with `gh release upload --clobber` precisely so a re-run on the same tag is
idempotent. The supported shape (see softprops/action-gh-release docs) is to
upload every asset to a DRAFT release and publish the draft once complete, with
downstream consumers subscribing to `release.published` rather than
`release.prereleased`. Worth doing deliberately, with a dry run on a throwaway
tag first. *Trigger: any change to the mirror step, or the first time an asset
under a published tag needs to be treated as tamper-evident by a third party.*

