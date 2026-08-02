package store

import (
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// InterruptedToolResultText is the LLM-bound text of a synthesized result for
// a tool call that was never answered because the agent process died.
//
// It is deliberately shaped as an out-of-band `<system>` annotation (the same
// convention as providers.emptyToolResultMarker) so it cannot be mistaken for
// output the tool itself produced, and its wording separates "we don't know if
// this ran" from "this failed" — a genuine tool failure reports what went
// wrong, this reports that the outcome is unobservable.
const InterruptedToolResultText = "<system>Tool call interrupted: the agent process was terminated before this tool returned. " +
	"The tool MAY OR MAY NOT have executed — any side effects (file writes, commands run, network requests) are UNKNOWN. " +
	"Do not assume either outcome; verify the current state before retrying.</system>"

// interruptedToolResultDetailsKey marks a tool result as synthesized by
// SynthesizeInterruptedToolResults rather than produced by a real execution.
// Carried in ToolResultMessage.Details, which is internal metadata (never sent
// to the LLM), so callers can tell a reconstructed result from a real one
// without string-matching the content. Producer (newInterruptedToolResult) and
// consumer (IsInterruptedToolResult) are paired on this map[string]any shape;
// the value never crosses a JSON round-trip because it is never persisted.
const interruptedToolResultDetailsKey = "interruptedToolCall"

// IsInterruptedToolResult reports whether a tool result was synthesized for a
// tool call orphaned by process death, as opposed to being the recorded
// outcome of a real tool execution.
func IsInterruptedToolResult(tr *ai.ToolResultMessage) bool {
	if tr == nil {
		return false
	}
	d, ok := tr.Details.(map[string]any)
	if !ok {
		return false
	}
	v, _ := d[interruptedToolResultDetailsKey].(bool)
	return v
}

// newInterruptedToolResult builds the synthetic error result for one orphaned
// tool call. Timestamp is inherited from the assistant message that issued the
// call rather than read from the clock: the output must be byte-identical on
// every load so provider prompt caches for the suffix stay valid.
func newInterruptedToolResult(tc *ai.ToolCall, timestamp int64) agent.AgentMessage {
	return agent.NewAgentMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		Role:       ai.RoleToolResult,
		ToolCallID: tc.ID,
		ToolName:   tc.Name,
		Content: []ai.ToolResultContent{
			{Type: ai.ContentTypeText, Text: InterruptedToolResultText},
		},
		Details:   map[string]any{interruptedToolResultDetailsKey: true},
		IsError:   true,
		Timestamp: timestamp,
	}))
}

// SynthesizeInterruptedToolResults returns msgs with an error tool result
// inserted for every assistant tool call that has no matching tool result
// anywhere in the list.
//
// This is the resume-time repair for a process killed mid tool call: the
// transcript's last assistant message carries one or more `toolCall` blocks
// and no `toolResult` ever got written. Without a stand-in the model has no
// idea whether the tool ran and will typically just re-run it, duplicating
// side effects.
//
// Properties:
//   - Handles several parallel tool calls in one assistant message.
//   - Position-independent: an orphan mid-history is repaired the same way as
//     one at the tail. Insertion is immediately after the owning assistant
//     message, which is where providers (Anthropic in particular) require the
//     matching tool result to sit.
//   - Tool calls that already have a result ANYWHERE in the list are left
//     alone, even if that result appears out of order.
//   - Assistant messages with StopReason error/aborted are skipped, mirroring
//     providers.TransformMessages, which drops those messages entirely. Adding
//     a result for a tool call that gets dropped downstream would create the
//     inverse defect — an orphaned tool_result.
//   - Pure and idempotent: input is never mutated, and a second pass over the
//     output is a no-op.
//
// The synthesized message is in-memory only; it is never written back to the
// transcript. See docs and CHANGELOG for the rationale.
func SynthesizeInterruptedToolResults(msgs []agent.AgentMessage) []agent.AgentMessage {
	// First pass: every tool call id that is already answered, at any position.
	if len(msgs) == 0 {
		return msgs
	}
	answered := make(map[string]bool)
	for i := range msgs {
		if msgs[i].Custom != nil {
			continue
		}
		if tr := msgs[i].Message.AsToolResult(); tr != nil {
			answered[tr.ToolCallID] = true
		}
	}

	// Second pass: emit, inserting a synthetic result after each assistant
	// message that left calls unanswered.
	var out []agent.AgentMessage
	for i := range msgs {
		m := msgs[i]
		out = append(out, m)

		a := m.Message.AsAssistant()
		if a == nil || m.Custom != nil {
			continue
		}
		if a.StopReason == ai.StopReasonError || a.StopReason == ai.StopReasonAborted {
			continue
		}
		for j := range a.Content {
			tc := a.Content[j].ToolCall
			if tc == nil || answered[tc.ID] {
				continue
			}
			out = append(out, newInterruptedToolResult(tc, a.Timestamp))
			// Guard against a malformed message repeating the same id.
			answered[tc.ID] = true
		}
	}
	return out
}
