package compaction

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestSerializeConversation(t *testing.T) {
	messages := []ai.Message{
		ai.NewUserMsg("Hello, can you help me?", 100),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewTextContent("Sure, let me take a look."),
			},
		}),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewThinkingContent("Let me think about this..."),
				ai.NewToolCallContent("1", "read", map[string]any{"path": "/main.go"}),
			},
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "read",
			Content: []ai.ToolResultContent{
				{Type: "text", Text: "package main"},
			},
		}),
	}

	result := SerializeConversation(messages)

	if !strings.Contains(result, "[User]: Hello, can you help me?") {
		t.Error("expected user message in output")
	}
	if !strings.Contains(result, "[Assistant]: Sure, let me take a look.") {
		t.Error("expected assistant text in output")
	}
	// Thinking blocks are intentionally dropped from summarizer input —
	// see Phase 1 #6 of docs/review-compact-flow/COMPACTION_REWORK.md.
	if strings.Contains(result, "[Assistant thinking]") {
		t.Error("expected thinking to be dropped from summarizer input")
	}
	if !strings.Contains(result, "[Assistant tool calls]: read(") {
		t.Error("expected tool calls in output")
	}
	if !strings.Contains(result, "[Tool result]: package main") {
		t.Error("expected tool result in output")
	}
}

func TestSerializeConversation_Empty(t *testing.T) {
	result := SerializeConversation(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestSummarizationSystemPrompt(t *testing.T) {
	if SummarizationSystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
	if !strings.Contains(SummarizationSystemPrompt, "summarization") {
		t.Error("expected 'summarization' in system prompt")
	}
}
