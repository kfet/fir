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
