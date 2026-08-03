# Backlog

Tracked follow-ups for fir. Items here are non-urgent but should not be lost.

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
