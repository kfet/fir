package compaction

import (
	"strings"
	"testing"
	"unicode/utf8"

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

func TestSerializeConversation_StubUTF8Safe(t *testing.T) {
	// 6000 bytes of repeated 3-byte rune ("中"). A naïve byte slice
	// could split a rune mid-sequence; the rune-aware truncation must
	// preserve valid UTF-8 in head and tail.
	bigText := strings.Repeat("中", 2000)
	messages := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "bash",
			Content:    []ai.ToolResultContent{{Type: "text", Text: bigText}},
		}),
	}
	out := SerializeConversationWithIDs(messages, []string{"e1"}, DefaultStubOptions)
	if !utf8.ValidString(out) {
		t.Fatalf("stub output is not valid UTF-8: %q", out)
	}
	if !strings.Contains(out, "entry e1") {
		t.Errorf("expected entry id in stub: %q", out)
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

func TestSerializeConversation_StubsLargeToolResult(t *testing.T) {
	bigText := strings.Repeat("x", 5000)
	messages := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "bash",
			Content: []ai.ToolResultContent{
				{Type: "text", Text: bigText},
			},
		}),
	}
	out := SerializeConversationWithIDs(messages, []string{"e7f3a2"}, DefaultStubOptions)
	if !strings.Contains(out, "entry e7f3a2") {
		t.Errorf("expected entry id in stub, got %q", out)
	}
	if !strings.Contains(out, "tool=bash") {
		t.Errorf("expected tool name in stub, got %q", out)
	}
	if !strings.Contains(out, "bytes=5000") {
		t.Errorf("expected bytes in stub, got %q", out)
	}
	if strings.Contains(out, bigText) {
		t.Error("expected full body to be elided")
	}
}

func TestSerializeConversation_KeepsSmallToolResult(t *testing.T) {
	messages := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "read",
			Content: []ai.ToolResultContent{
				{Type: "text", Text: "small output"},
			},
		}),
	}
	out := SerializeConversationWithIDs(messages, []string{"abc"}, DefaultStubOptions)
	if !strings.Contains(out, "[Tool result]: small output") {
		t.Errorf("expected un-stubbed small result, got %q", out)
	}
	if strings.Contains(out, "entry abc") {
		t.Error("did not expect stub for small result")
	}
}
