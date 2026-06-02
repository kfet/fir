package agent

import "github.com/kfet/fir/pkg/ai"

// StripUnmatchedToolCalls returns a copy of msgs with every assistant
// tool-call content block removed when no ToolResult message in msgs carries a
// matching ToolCallID. Assistant messages left with no content blocks are
// dropped entirely. All other messages pass through unchanged, and the input
// slice and its messages are never mutated.
//
// This sanitizes a message snapshot for a one-shot side query. The snapshot is
// taken from live session state, which — because the assistant turn is
// committed on EventMessageEnd before its tools execute — can end with an
// in-flight tool call that has no result yet (notably the very `aside`
// invocation driving the side query). Appending a user question after such a
// dangling tool_use produces a malformed context: the model role-plays a
// continuation of the executor's turn (e.g. narrating that the tool "failed")
// instead of answering the question. Stripping the unmatched calls yields a
// well-formed context that ends on a complete turn.
func StripUnmatchedToolCalls(msgs []AgentMessage) []AgentMessage {
	if len(msgs) == 0 {
		return msgs
	}

	// Collect every tool-result id present in the snapshot.
	resultIDs := make(map[string]struct{})
	for i := range msgs {
		if tr := msgs[i].Message.AsToolResult(); tr != nil {
			resultIDs[tr.ToolCallID] = struct{}{}
		}
	}

	out := make([]AgentMessage, 0, len(msgs))
	for i := range msgs {
		am := msgs[i].Message.AsAssistant()
		if am == nil {
			out = append(out, msgs[i])
			continue
		}

		// Fast path: keep the message untouched unless it has an unmatched
		// tool call.
		hasUnmatched := false
		for j := range am.Content {
			if tc := am.Content[j].ToolCall; tc != nil {
				if _, ok := resultIDs[tc.ID]; !ok {
					hasUnmatched = true
					break
				}
			}
		}
		if !hasUnmatched {
			out = append(out, msgs[i])
			continue
		}

		// Rebuild without the unmatched tool calls. Snapshot first so the
		// shared, live message is never mutated.
		snap := am.SnapshotContent()
		filtered := make([]ai.AssistantContent, 0, len(snap.Content))
		for _, c := range snap.Content {
			if tc := c.ToolCall; tc != nil {
				if _, ok := resultIDs[tc.ID]; !ok {
					continue
				}
			}
			filtered = append(filtered, c)
		}
		if len(filtered) == 0 {
			// The message held only unmatched tool calls — drop it.
			continue
		}
		snap.Content = filtered
		if !hasSubstantiveContent(filtered) {
			// Only thinking blocks remain after stripping the unmatched
			// calls — a non-actionable in-flight remnant. A lone thinking
			// block before the appended user question is useless context and
			// risks the same role-play; drop the message. Safe: there is no
			// remaining tool_use the thinking must stay paired with.
			continue
		}
		out = append(out, NewAgentMessage(ai.NewAssistantMsg(*snap)))
	}
	return out
}

// hasSubstantiveContent reports whether content carries any block that can
// stand on its own in a turn — text, a tool call, or server content. A
// thinking block alone is not substantive: it only makes sense paired with a
// following text or tool_use block.
func hasSubstantiveContent(content []ai.AssistantContent) bool {
	for _, c := range content {
		if c.Text != nil || c.ToolCall != nil || c.Server != nil {
			return true
		}
	}
	return false
}
