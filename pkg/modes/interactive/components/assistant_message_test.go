package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestAssistantMessageComponent_TextContent(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			{Text: &ai.TextContent{Type: "text", Text: "Hello from assistant"}},
		},
		StopReason: ai.StopReasonStop,
	}
	comp := NewAssistantMessageComponent(msg, false, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Hello from assistant") {
		t.Errorf("expected text content, got %q", joined)
	}
}

func TestAssistantMessageComponent_ThinkingHidden(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{Type: "thinking", Thinking: "Deep thoughts"}},
			{Text: &ai.TextContent{Type: "text", Text: "Final answer"}},
		},
		StopReason: ai.StopReasonStop,
	}
	comp := NewAssistantMessageComponent(msg, true, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Thinking...") {
		t.Errorf("expected 'Thinking...' label when hidden, got %q", joined)
	}
	if strings.Contains(joined, "Deep thoughts") {
		t.Errorf("did not expect thinking content when hidden")
	}
}

func TestAssistantMessageComponent_ThinkingShown(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{Type: "thinking", Thinking: "Deep thoughts"}},
		},
		StopReason: ai.StopReasonStop,
	}
	comp := NewAssistantMessageComponent(msg, false, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Deep thoughts") {
		t.Errorf("expected thinking content, got %q", joined)
	}
}

func TestAssistantMessageComponent_Aborted(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role:       "assistant",
		Content:    []ai.AssistantContent{},
		StopReason: ai.StopReasonAborted,
	}
	comp := NewAssistantMessageComponent(msg, false, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "aborted") {
		t.Errorf("expected abort message, got %q", joined)
	}
}

func TestAssistantMessageComponent_Error(t *testing.T) {
	msg := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		StopReason:   ai.StopReasonError,
		ErrorMessage: "something went wrong",
	}
	comp := NewAssistantMessageComponent(msg, false, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "something went wrong") {
		t.Errorf("expected error message, got %q", joined)
	}
}

func TestAssistantMessageComponent_NilMessage(t *testing.T) {
	comp := NewAssistantMessageComponent(nil, false, nil)
	lines := comp.Render(80)
	// Should not panic, may render empty
	_ = lines
}
