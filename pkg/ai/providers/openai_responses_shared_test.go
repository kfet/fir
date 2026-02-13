package providers

import (
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
)

func TestResponsesSSEProcessor_TextStream(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	model := &ai.Model{ID: "test"}
	proc := &responsesSSEProcessor{output: output, stream: stream, model: model}

	// Simulate output_item.added (message)
	done, err := proc.processEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant"}}`)
	if err != nil || done {
		t.Fatalf("unexpected: done=%v err=%v", done, err)
	}
	if len(output.Content) != 1 || !output.Content[0].IsText() {
		t.Fatal("expected one text content block")
	}

	// Simulate text delta
	done, err = proc.processEvent(`{"type":"response.output_text.delta","delta":"Hello "}`)
	if err != nil || done {
		t.Fatalf("unexpected: done=%v err=%v", done, err)
	}
	if output.Content[0].Text.Text != "Hello " {
		t.Errorf("expected 'Hello ', got %q", output.Content[0].Text.Text)
	}

	// Another delta
	proc.processEvent(`{"type":"response.output_text.delta","delta":"world!"}`)
	if output.Content[0].Text.Text != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", output.Content[0].Text.Text)
	}

	// output_item.done
	proc.processEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"Hello world!"}]}}`)
	if output.Content[0].Text.TextSignature != "msg_1" {
		t.Errorf("expected text signature 'msg_1', got %q", output.Content[0].Text.TextSignature)
	}
}

func TestResponsesSSEProcessor_ToolCall(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	model := &ai.Model{ID: "test"}
	proc := &responsesSSEProcessor{output: output, stream: stream, model: model}

	// function_call added
	proc.processEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read"}}`)
	if len(output.Content) != 1 || !output.Content[0].IsToolCall() {
		t.Fatal("expected one tool call block")
	}
	if output.Content[0].ToolCall.Name != "read" {
		t.Errorf("expected tool name 'read', got %q", output.Content[0].ToolCall.Name)
	}
	if output.Content[0].ToolCall.ID != "call_1|fc_1" {
		t.Errorf("expected combined ID 'call_1|fc_1', got %q", output.Content[0].ToolCall.ID)
	}

	// args delta
	proc.processEvent(`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\"te"}`)
	proc.processEvent(`{"type":"response.function_call_arguments.delta","delta":"st.txt\"}"}`)

	// args done
	proc.processEvent(`{"type":"response.function_call_arguments.done","arguments":"{\"path\":\"test.txt\"}"}`)
	if output.Content[0].ToolCall.Arguments["path"] != "test.txt" {
		t.Errorf("expected path='test.txt', got %v", output.Content[0].ToolCall.Arguments["path"])
	}
}

func TestResponsesSSEProcessor_Error(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	model := &ai.Model{ID: "test"}
	proc := &responsesSSEProcessor{output: output, stream: stream, model: model}

	done, err := proc.processEvent(`{"type":"error","code":"rate_limit","message":"Too many requests"}`)
	if !done {
		t.Error("expected done=true on error")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "Error Code rate_limit: Too many requests" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResponsesSSEProcessor_Completed(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	model := &ai.Model{ID: "test", Cost: ai.ModelCost{Input: 1.0, Output: 2.0}}
	proc := &responsesSSEProcessor{output: output, stream: stream, model: model}

	proc.processEvent(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150,"input_tokens_details":{"cached_tokens":20}}}}`)

	if output.StopReason != ai.StopReasonStop {
		t.Errorf("expected stop reason 'stop', got %q", output.StopReason)
	}
	if output.Usage.Input != 80 {
		t.Errorf("expected input=80 (100-20 cached), got %d", output.Usage.Input)
	}
	if output.Usage.Output != 50 {
		t.Errorf("expected output=50, got %d", output.Usage.Output)
	}
	if output.Usage.CacheRead != 20 {
		t.Errorf("expected cacheRead=20, got %d", output.Usage.CacheRead)
	}
}

func TestResponsesSSEProcessor_SkipsInvalidJSON(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	proc := &responsesSSEProcessor{output: output, stream: stream, model: &ai.Model{}}

	done, err := proc.processEvent("{invalid json")
	if done || err != nil {
		t.Errorf("expected skip for invalid JSON, got done=%v err=%v", done, err)
	}

	done, err = proc.processEvent("")
	if done || err != nil {
		t.Errorf("expected skip for empty data, got done=%v err=%v", done, err)
	}

	done, err = proc.processEvent("[DONE]")
	if done || err != nil {
		t.Errorf("expected skip for [DONE], got done=%v err=%v", done, err)
	}
}

func TestConvertResponsesTools(t *testing.T) {
	tools := []ai.Tool{
		{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object"}},
		{Name: "write", Description: "Write a file", Parameters: map[string]any{"type": "object"}},
	}

	result := convertResponsesTools(tools, false)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}
	if result[0]["name"] != "read" {
		t.Errorf("expected 'read', got %v", result[0]["name"])
	}
	if result[0]["strict"] != false {
		t.Errorf("expected strict=false")
	}

	// With strict
	result = convertResponsesTools(tools, true)
	if result[0]["strict"] != true {
		t.Errorf("expected strict=true")
	}
}

func TestNormalizeResponsesToolCallID_NoChange(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "other-provider"}
	got := normalizeResponsesToolCallID("some-id", model, nil)
	if got != "some-id" {
		t.Errorf("expected no change for non-responses provider, got %q", got)
	}
}

func TestNormalizeResponsesToolCallID_NoPipe(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	got := normalizeResponsesToolCallID("call_123", model, nil)
	if got != "call_123" {
		t.Errorf("expected no change for ID without pipe, got %q", got)
	}
}

func TestNormalizeResponsesToolCallID_WithPipe(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	got := normalizeResponsesToolCallID("call_123|fc_456", model, nil)
	if got != "call_123|fc_456" {
		t.Errorf("expected 'call_123|fc_456', got %q", got)
	}
}

func TestNormalizeResponsesToolCallID_AddFcPrefix(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	got := normalizeResponsesToolCallID("call_123|item_456", model, nil)
	if got != "call_123|fc_item_456" {
		t.Errorf("expected 'call_123|fc_item_456', got %q", got)
	}
}

func TestNormalizeResponsesToolCallID_Sanitize(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	got := normalizeResponsesToolCallID("call.123|fc.456", model, nil)
	if got != "call_123|fc_456" {
		t.Errorf("expected sanitized IDs, got %q", got)
	}
}

func TestNormalizeResponsesToolCallID_Truncate(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	longID := strings.Repeat("a", 100) + "|fc_" + strings.Repeat("b", 100)
	got := normalizeResponsesToolCallID(longID, model, nil)
	parts := strings.SplitN(got, "|", 2)
	if len(parts[0]) > 64 {
		t.Errorf("callID should be truncated to 64, got %d", len(parts[0]))
	}
	if len(parts[1]) > 64 {
		t.Errorf("itemID should be truncated to 64, got %d", len(parts[1]))
	}
}

func TestNormalizeResponsesToolCallID_StripTrailingUnderscores(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "openai"}
	got := normalizeResponsesToolCallID("call___||fc___", model, nil)
	parts := strings.SplitN(got, "|", 2)
	if strings.HasSuffix(parts[0], "_") {
		t.Error("callID should not end with underscore")
	}
}

func TestSanitizeIDChars(t *testing.T) {
	if sanitizeIDChars("abc-123_XYZ") != "abc-123_XYZ" {
		t.Error("should preserve valid chars")
	}
	if sanitizeIDChars("a.b+c") != "a_b_c" {
		t.Errorf("should replace invalid chars: got %q", sanitizeIDChars("a.b+c"))
	}
}

func TestShortHash(t *testing.T) {
	h1 := shortHash("hello")
	h2 := shortHash("world")
	if h1 == "" || h2 == "" {
		t.Error("expected non-empty hashes")
	}
	if h1 == h2 {
		t.Error("expected different hashes for different inputs")
	}
	// Same input produces same hash
	if shortHash("hello") != h1 {
		t.Error("expected deterministic hash")
	}
}
