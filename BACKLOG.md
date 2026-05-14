# Backlog

Tracked follow-ups for fir. Items here are non-urgent but should not be lost.

## Generic passthrough content variant for server-side blocks

**Why.** Anthropic emits server-side content blocks (`server_tool_use`,
`web_search_tool_result`, `code_execution_tool_result`,
`web_fetch_tool_result`, `tool_invocation`, `tool_output`, plus any future
ones) that fir currently flattens into `text` blocks during streaming. This
loses information at the storage boundary: we cannot reconstruct the
original block on replay. The flattening is what produced the
`thinking blocks cannot be modified` 400 fixed in v0.46.3 — after the
pruner dropped an empty `server_tool_use` placeholder, two signed
thinking blocks ended up adjacent on disk and Anthropic's input
validator rejected the resulting wire shape on replay.

v0.46.3 ships two band-aids: a non-empty placeholder for
`server_tool_use` (so it survives the pruner), and
`separateAdjacentThinkingBlocks` that splices a synthetic text separator
at wire-build time if adjacent thinkings ever slip through anyway. These
keep us correct but are not the right shape long-term.

**The right shape.**

1. Add a new variant to `ai.AssistantContent`:

   ```go
   type ServerContent struct {
       Type string         // "server_tool_use", "web_search_tool_result", …
       Raw  map[string]any // the entire content_block payload, verbatim
   }
   ```

   Plus helpers `IsServerContent()`, `NewServerContent(...)`.

2. **Streaming side** (`pkg/ai/providers/anthropic.go`,
   `content_block_start` switch): replace the six special cases for
   `server_tool_use`, `web_search_tool_result`,
   `code_execution_tool_result`, `web_fetch_tool_result`,
   `tool_invocation`, `tool_output` with a single default branch that
   stores the raw `cb` map as `ServerContent`. New block types Anthropic
   ships later round-trip automatically.

3. **Wire side** (`convertAnthropicMessages`, assistant block builder):
   emit `ServerContent.Raw` verbatim as the wire block. This means the
   exact `type` and all sibling fields go back to Anthropic as it sent
   them — restoring the structural property that originally separated
   adjacent thinking blocks.

4. **Display side** — the existing `formatWebSearchResult`,
   `formatCodeExecutionResult`, etc. helpers continue to render the
   blocks for the TUI/ACP, but at *display time* rather than baking
   formatted text into stored Content. Add an `EventServerContent`
   event so consumers can render without round-tripping through text
   deltas, or have the streamer continue to emit synthetic text deltas
   for display only with a marker indicating they are display-only.

5. **Persistence** — JSON round-trip for `ServerContent` (the `Raw` map
   serialises straightforwardly). Old sessions that lack the new variant
   continue to load as text-flattened content; the band-aids from v0.46.3
   cover them.

6. **Remove band-aids** — once nothing in stored history can produce
   adjacent thinking blocks, `separateAdjacentThinkingBlocks` and the
   placeholder-text trick in the streamer can both be deleted. The
   regression tests (`anthropic_adjacent_thinking_test.go`) should be
   retained, switched to assert structural integrity through the new
   passthrough path.

**Scope estimate.** ~200-350 lines. Touches `pkg/ai/types.go`, the
Anthropic stream parser, `convertAnthropicMessages`, TUI / ACP display,
plus tests.

**Reporting upstream.** Anthropic's API exhibits an inconsistent
contract: the streaming response emits assistant content shapes that
its own input validator will then reject on replay (consecutive
`thinking` blocks). Worth filing once the proper fix lands so we can
remove the band-aid with confidence rather than guessing whether
Anthropic has changed the validator priority.
