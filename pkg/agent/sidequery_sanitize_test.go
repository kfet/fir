package agent

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// asstMsg builds an assistant AgentMessage from content blocks.
func asstMsg(content ...ai.AssistantContent) AgentMessage {
	return NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:    "assistant",
		Content: content,
	}))
}

func userMsg(text string) AgentMessage {
	return NewAgentMessage(ai.NewUserMsg(text, 0))
}

func toolResultMsg(id string) AgentMessage {
	return NewAgentMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		Role:       "toolResult",
		ToolCallID: id,
		Content:    []ai.ToolResultContent{{Type: "text", Text: "ok"}},
	}))
}

func toolCallBlocks(am AgentMessage) []string {
	var ids []string
	a := am.Message.AsAssistant()
	if a == nil {
		return ids
	}
	for _, c := range a.Content {
		if c.ToolCall != nil {
			ids = append(ids, c.ToolCall.ID)
		}
	}
	return ids
}

func TestStripUnmatchedToolCalls_RemovesInFlightCall(t *testing.T) {
	// Mirrors the aside bug: an in-flight assistant turn carries two tool
	// calls (plan + aside). plan already has its result appended; aside (the
	// call driving the side query) does not.
	msgs := []AgentMessage{
		userMsg("do the task"),
		asstMsg(
			ai.NewTextContent("Let me plan and escalate."),
			ai.NewToolCallContent("plan_1", "plan", map[string]any{}),
			ai.NewToolCallContent("aside_1", "aside", map[string]any{"escalate": true}),
		),
		toolResultMsg("plan_1"),
	}

	got := StripUnmatchedToolCalls(msgs)

	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	// The assistant message must keep the matched plan call and its text,
	// but drop the unmatched aside call.
	ids := toolCallBlocks(got[1])
	if len(ids) != 1 || ids[0] != "plan_1" {
		t.Fatalf("expected only matched plan_1 tool call, got %v", ids)
	}
	// Text content must be preserved.
	if a := got[1].Message.AsAssistant(); a == nil || len(a.Content) != 2 {
		t.Fatalf("expected text + plan call preserved, got %+v", got[1])
	}
}

func TestStripUnmatchedToolCalls_DropsEmptyAssistantMessage(t *testing.T) {
	// The trailing assistant message contains only an unmatched tool call —
	// after stripping it the message is empty and must be dropped entirely.
	msgs := []AgentMessage{
		userMsg("hi"),
		asstMsg(ai.NewToolCallContent("aside_1", "aside", map[string]any{})),
	}

	got := StripUnmatchedToolCalls(msgs)

	if len(got) != 1 {
		t.Fatalf("expected dangling-only assistant message dropped, got %d messages", len(got))
	}
	if got[0].Message.AsUser() == nil {
		t.Fatalf("expected remaining message to be the user message")
	}
}

func TestStripUnmatchedToolCalls_KeepsMatchedCalls(t *testing.T) {
	// A fully-resolved history must pass through unchanged.
	msgs := []AgentMessage{
		userMsg("hi"),
		asstMsg(
			ai.NewTextContent("calling tool"),
			ai.NewToolCallContent("t1", "read", map[string]any{}),
		),
		toolResultMsg("t1"),
		asstMsg(ai.NewTextContent("done")),
	}

	got := StripUnmatchedToolCalls(msgs)

	if len(got) != 4 {
		t.Fatalf("expected unchanged 4 messages, got %d", len(got))
	}
	if ids := toolCallBlocks(got[1]); len(ids) != 1 || ids[0] != "t1" {
		t.Fatalf("expected matched call preserved, got %v", ids)
	}
}

func TestStripUnmatchedToolCalls_DoesNotMutateInput(t *testing.T) {
	orig := asstMsg(
		ai.NewTextContent("x"),
		ai.NewToolCallContent("a", "aside", map[string]any{}),
	)
	msgs := []AgentMessage{userMsg("hi"), orig}

	_ = StripUnmatchedToolCalls(msgs)

	// Original assistant message must still carry both content blocks.
	if a := msgs[1].Message.AsAssistant(); a == nil || len(a.Content) != 2 {
		t.Fatalf("input was mutated: %+v", msgs[1])
	}
}

func TestStripUnmatchedToolCalls_DropsThinkingOnlyLeftover(t *testing.T) {
	// Stripping the unmatched call leaves only a thinking block — a
	// non-actionable in-flight remnant that must be dropped, not kept as a
	// lone thinking-only assistant message before the appended user turn.
	think := ai.AssistantContent{Thinking: &ai.ThinkingContent{
		Thinking:          "deliberating",
		ThinkingSignature: "sig",
	}}
	msgs := []AgentMessage{
		userMsg("hi"),
		asstMsg(think, ai.NewToolCallContent("aside_1", "aside", map[string]any{})),
	}

	got := StripUnmatchedToolCalls(msgs)

	if len(got) != 1 {
		t.Fatalf("expected thinking-only leftover dropped, got %d messages", len(got))
	}
	if got[0].Message.AsUser() == nil {
		t.Fatalf("expected remaining message to be the user message")
	}
}

func TestStripUnmatchedToolCalls_KeepsThinkingWithText(t *testing.T) {
	// When text survives alongside the stripped call, the message (with its
	// thinking) is kept — it's a substantive, complete turn.
	think := ai.AssistantContent{Thinking: &ai.ThinkingContent{Thinking: "x", ThinkingSignature: "s"}}
	msgs := []AgentMessage{
		userMsg("hi"),
		asstMsg(think, ai.NewTextContent("here goes"), ai.NewToolCallContent("aside_1", "aside", map[string]any{})),
	}

	got := StripUnmatchedToolCalls(msgs)

	if len(got) != 2 {
		t.Fatalf("expected text-bearing message kept, got %d messages", len(got))
	}
	a := got[1].Message.AsAssistant()
	if a == nil || len(a.Content) != 2 {
		t.Fatalf("expected thinking+text preserved, got %+v", got[1])
	}
}

func TestStripUnmatchedToolCalls_Empty(t *testing.T) {
	if got := StripUnmatchedToolCalls(nil); got != nil && len(got) != 0 {
		t.Fatalf("expected empty result for nil input, got %v", got)
	}
}
