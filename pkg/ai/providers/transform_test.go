// Ported from: packages/ai/src/providers/transform-messages.ts
// Upstream hash: 1caadb2e
package providers

import (
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
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

func TestTransformMessages_SyntheticToolResultUsesAssistantTimestamp(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", Provider: "anthropic", Api: "anthropic-messages"}
	assistantTS := int64(1700000000000)
	msgs := []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider:   "anthropic",
			Api:        "anthropic-messages",
			Model:      "claude-sonnet",
			StopReason: ai.StopReasonToolUse,
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("call-1", "bash", map[string]any{"command": "ls"}),
			},
			Timestamp: assistantTS,
		}),
		// No tool result — orphaned tool call
		ai.NewUserMsg("next message", 1700000001000),
	}

	result := TransformMessages(msgs, model, nil)

	// Should have: assistant, synthetic tool result, user
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	tr := result[1].AsToolResult()
	if tr == nil {
		t.Fatal("expected message[1] to be a tool result")
	}
	if tr.Timestamp != assistantTS {
		t.Errorf("synthetic tool result should use assistant timestamp %d, got %d", assistantTS, tr.Timestamp)
	}

	// Run again — timestamp must be identical (deterministic)
	result2 := TransformMessages(msgs, model, nil)
	tr2 := result2[1].AsToolResult()
	if tr2.Timestamp != tr.Timestamp {
		t.Errorf("synthetic tool result timestamp not deterministic: %d vs %d", tr.Timestamp, tr2.Timestamp)
	}
}

// TestTransformMessages_ThinkingSignatureSameProviderDifferentModelID checks
// that a thinking block with a valid signature is preserved verbatim when the
// stored assistant message comes from the same provider+API but a different
// model ID (e.g. alias "claude-opus-4-7" vs dated "claude-opus-4-7-20250514").
// Converting it to text would strip the signature and cause a 400 from the
// Anthropic API on the next turn ("thinking blocks cannot be modified").
func TestTransformMessages_ThinkingSignatureSameProviderDifferentModelID(t *testing.T) {
	// Current call uses the dated model ID.
	model := &ai.Model{
		ID:       "claude-opus-4-7-20250514",
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
	}
	// Stored assistant message uses the alias.
	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7", // different ID → isSameModel = false
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{
				Type:              ai.ContentTypeThinking,
				Thinking:          "let me think",
				ThinkingSignature: "sig-cross-model",
			}},
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
		t.Errorf("expected thinking block preserved, got %s", am.Content[0].ContentType())
	}
	if am.Content[0].Thinking.ThinkingSignature != "sig-cross-model" {
		t.Errorf("expected signature 'sig-cross-model', got %q", am.Content[0].Thinking.ThinkingSignature)
	}
}

// TestTransformMessages_ThinkingEmptySignatureDifferentModel verifies that a
// thinking block WITHOUT a signature from a different model is still converted
// to plain text (existing behaviour for cross-provider thinking downgrade).
func TestTransformMessages_ThinkingEmptySignatureDifferentModel(t *testing.T) {
	model := &ai.Model{ID: "gpt-4", Provider: ai.ProviderOpenAI, Api: ai.ApiOpenAICompletions}
	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-3",
		Content: []ai.AssistantContent{
			{Thinking: &ai.ThinkingContent{
				Type:     ai.ContentTypeThinking,
				Thinking: "some thought",
				// No ThinkingSignature
			}},
			ai.NewTextContent("answer"),
		},
		StopReason: ai.StopReasonStop,
	}

	result := TransformMessages([]ai.Message{ai.NewAssistantMsg(msg)}, model, nil)
	am := result[0].AsAssistant()
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 blocks (converted to text), got %d", len(am.Content))
	}
	if !am.Content[0].IsText() {
		t.Errorf("expected thinking converted to text, got %s", am.Content[0].ContentType())
	}
}

// TestTransformMessages_RedactedThinkingSameProviderDifferentModelID verifies
// that a redacted thinking block (Thinking="", ThinkingSignature="encrypted",
// Redacted=true) is preserved verbatim when isSameProvider=true but
// isSameModel=false.  The empty Thinking text must not trigger the drop-empty
// guard; only the ThinkingSignature check should apply.
func TestTransformMessages_RedactedThinkingSameProviderDifferentModelID(t *testing.T) {
	model := &ai.Model{
		ID:       "claude-opus-4-7-20250514",
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
	}
	redactedBlock := ai.NewThinkingContent("")
	redactedBlock.Thinking.Redacted = true
	redactedBlock.Thinking.ThinkingSignature = "EncryptedBlob=="

	msg := ai.AssistantMessage{
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7", // different ID → isSameModel = false
		Content: []ai.AssistantContent{
			redactedBlock,
			ai.NewTextContent("answer"),
		},
		StopReason: ai.StopReasonStop,
	}

	result := TransformMessages([]ai.Message{ai.NewAssistantMsg(msg)}, model, nil)
	am := result[0].AsAssistant()
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 blocks (redacted thinking + text), got %d", len(am.Content))
	}
	if !am.Content[0].IsThinking() {
		t.Errorf("expected redacted thinking block preserved, got %s", am.Content[0].ContentType())
	}
	if !am.Content[0].Thinking.Redacted {
		t.Error("expected Redacted=true to be preserved")
	}
	if am.Content[0].Thinking.ThinkingSignature != "EncryptedBlob==" {
		t.Errorf("expected ThinkingSignature preserved, got %q", am.Content[0].Thinking.ThinkingSignature)
	}
}
