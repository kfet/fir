// Ported from: packages/ai/src/providers/transform-messages.ts
// Upstream hash: 1caadb2e
package providers

import (
	"testing"
	"time"

	"github.com/kfet/tau/pkg/ai"
)

func TestTransformMessages_UserPassThrough(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	messages := []ai.Message{ai.NewUserMsg("hello", time.Now().UnixMilli())}
	result := TransformMessages(messages, model, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role() != ai.RoleUser {
		t.Errorf("expected user role, got %q", result[0].Role())
	}
}

func TestTransformMessages_ThinkingSameModel(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-3",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{Type: ai.ContentTypeThinking, Thinking: "let me think", ThinkingSignature: "sig-1"}},
			ai.NewTextContent("answer"),
		},
		StopReason: ai.StopReasonStop,
	}
	result := TransformMessages([]ai.Message{ai.NewAssistantMsg(msg)}, model, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	am := result[0].AsAssistant()
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(am.Content))
	}
	if !am.Content[0].IsThinking() {
		t.Error("expected thinking block at index 0")
	}
	if am.Content[0].Thinking.Thinking != "let me think" {
		t.Errorf("expected 'let me think', got %q", am.Content[0].Thinking.Thinking)
	}
	if am.Content[0].Thinking.ThinkingSignature != "sig-1" {
		t.Errorf("expected signature 'sig-1', got %q", am.Content[0].Thinking.ThinkingSignature)
	}
}

func TestTransformMessages_ThinkingDifferentModel(t *testing.T) {
	model := &ai.Model{ID: "gpt-4", Provider: ai.ProviderOpenAI, Api: ai.ApiOpenAICompletions}
	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-3",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{Type: ai.ContentTypeThinking, Thinking: "let me think"}},
			ai.NewTextContent("answer"),
		},
		StopReason: ai.StopReasonStop,
	}
	result := TransformMessages([]ai.Message{ai.NewAssistantMsg(msg)}, model, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	am := result[0].AsAssistant()
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(am.Content))
	}
	if !am.Content[0].IsText() {
		t.Error("expected thinking converted to text")
	}
	if am.Content[0].Text.Text != "let me think" {
		t.Errorf("expected 'let me think', got %q", am.Content[0].Text.Text)
	}
}

func TestTransformMessages_EmptyThinkingSkipped(t *testing.T) {
	model := &ai.Model{ID: "gpt-4", Provider: ai.ProviderOpenAI, Api: ai.ApiOpenAICompletions}
	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-3",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{Type: ai.ContentTypeThinking, Thinking: ""}},
			ai.NewTextContent("answer"),
		},
		StopReason: ai.StopReasonStop,
	}
	result := TransformMessages([]ai.Message{ai.NewAssistantMsg(msg)}, model, nil)
	am := result[0].AsAssistant()
	if len(am.Content) != 1 {
		t.Fatalf("expected 1 content (empty thinking removed), got %d", len(am.Content))
	}
}

func TestTransformMessages_SkipErroredAssistant(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	messages := []ai.Message{
		ai.NewUserMsg("hello", time.Now().UnixMilli()),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider:   ai.ProviderAnthropic,
			Api:        ai.ApiAnthropicMessages,
			Model:      "claude-3",
			Content:    []ai.AssistantContent{ai.NewTextContent("partial")},
			StopReason: ai.StopReasonError,
		}),
	}
	result := TransformMessages(messages, model, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (errored assistant skipped), got %d", len(result))
	}
}

func TestTransformMessages_SkipAbortedAssistant(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	messages := []ai.Message{
		ai.NewUserMsg("hello", time.Now().UnixMilli()),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider:   ai.ProviderAnthropic,
			Api:        ai.ApiAnthropicMessages,
			Model:      "claude-3",
			Content:    []ai.AssistantContent{ai.NewTextContent("partial")},
			StopReason: ai.StopReasonAborted,
		}),
	}
	result := TransformMessages(messages, model, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (aborted assistant skipped), got %d", len(result))
	}
}

func TestTransformMessages_SyntheticToolResults(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	messages := []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider: ai.ProviderAnthropic,
			Api:      ai.ApiAnthropicMessages,
			Model:    "claude-3",
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("tc-1", "read", map[string]any{}),
			},
			StopReason: ai.StopReasonToolUse,
		}),
		ai.NewUserMsg("skip that", time.Now().UnixMilli()),
	}

	result := TransformMessages(messages, model, nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (assistant + synthetic tool result + user), got %d", len(result))
	}

	tr := result[1].AsToolResult()
	if tr == nil {
		t.Fatal("expected tool result at index 1")
	}
	if tr.ToolCallID != "tc-1" {
		t.Errorf("expected 'tc-1', got %q", tr.ToolCallID)
	}
	if !tr.IsError {
		t.Error("expected synthetic tool result to be an error")
	}
}

func TestTransformMessages_ToolCallIDNormalization(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	normalizer := func(id string, model *ai.Model, source *ai.AssistantMessage) string {
		return "normalized-" + id
	}

	messages := []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider: ai.ProviderOpenAI,
			Api:      ai.ApiOpenAICompletions,
			Model:    "gpt-4",
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("original-id", "read", map[string]any{}),
			},
			StopReason: ai.StopReasonToolUse,
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "original-id",
			ToolName:   "read",
			Content:    []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "data"}},
		}),
	}

	result := TransformMessages(messages, model, normalizer)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	am := result[0].AsAssistant()
	if !am.Content[0].IsToolCall() {
		t.Error("expected tool call")
	}
	if am.Content[0].ToolCall.ID != "normalized-original-id" {
		t.Errorf("expected 'normalized-original-id', got %q", am.Content[0].ToolCall.ID)
	}

	tr := result[1].AsToolResult()
	if tr.ToolCallID != "normalized-original-id" {
		t.Errorf("expected 'normalized-original-id', got %q", tr.ToolCallID)
	}
}

func TestTransformMessages_ThoughtSignatureStripped(t *testing.T) {
	model := &ai.Model{ID: "claude-3", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages}
	messages := []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider: ai.ProviderGoogle,
			Api:      ai.ApiGoogleGenerativeAI,
			Model:    "gemini-pro",
			Content: []ai.AssistantContent{
				{ToolCall: &ai.ToolCall{Type: ai.ContentTypeToolCall, ID: "tc-1", Name: "read", Arguments: map[string]any{}, ThoughtSignature: "thought-sig"}},
			},
			StopReason: ai.StopReasonToolUse,
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "tc-1",
			ToolName:   "read",
			Content:    []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "data"}},
		}),
	}

	result := TransformMessages(messages, model, nil)
	am := result[0].AsAssistant()
	if !am.Content[0].IsToolCall() {
		t.Error("expected tool call")
	}
	if am.Content[0].ToolCall.ThoughtSignature != "" {
		t.Errorf("expected empty thought signature, got %q", am.Content[0].ToolCall.ThoughtSignature)
	}
}
