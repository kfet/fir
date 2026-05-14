// Ported from: packages/ai/src/types.ts
// Upstream hash: 1caadb2e
package ai

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAssistantContent_Text(t *testing.T) {
	c := NewTextContent("hello")
	if !c.IsText() {
		t.Error("expected IsText")
	}
	if c.Text.Text != "hello" {
		t.Errorf("expected 'hello', got %q", c.Text.Text)
	}
	if c.IsThinking() || c.IsToolCall() {
		t.Error("unexpected type")
	}
}

func TestAssistantContent_Thinking(t *testing.T) {
	c := NewThinkingContent("hmm")
	if !c.IsThinking() {
		t.Error("expected IsThinking")
	}
	if c.Thinking.Thinking != "hmm" {
		t.Errorf("expected 'hmm', got %q", c.Thinking.Thinking)
	}
}

func TestAssistantContent_ToolCall(t *testing.T) {
	c := NewToolCallContent("tc1", "read", map[string]any{"path": "foo.txt"})
	if !c.IsToolCall() {
		t.Error("expected IsToolCall")
	}
	if c.ToolCall.Name != "read" {
		t.Errorf("expected 'read', got %q", c.ToolCall.Name)
	}
}

func TestAssistantContent_ContentType(t *testing.T) {
	tests := []struct {
		name string
		c    AssistantContent
		want string
	}{
		{"text", NewTextContent("hi"), ContentTypeText},
		{"thinking", NewThinkingContent("hmm"), ContentTypeThinking},
		{"toolCall", NewToolCallContent("tc1", "bash", nil), ContentTypeToolCall},
		{"empty", AssistantContent{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.c.ContentType()
			if got != tt.want {
				t.Errorf("ContentType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAssistantContent_JSON_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		c    AssistantContent
	}{
		{"text", NewTextContent("hello")},
		{"thinking", AssistantContent{Thinking: &ThinkingContent{Type: "thinking", Thinking: "hmm", ThinkingSignature: "sig1"}}},
		{"toolCall", NewToolCallContent("tc1", "bash", map[string]any{"cmd": "ls"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.c)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got AssistantContent
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.ContentType() != tt.c.ContentType() {
				t.Errorf("type mismatch: got %q, want %q", got.ContentType(), tt.c.ContentType())
			}
		})
	}
}

func TestMessage_RoundTrip(t *testing.T) {
	msgs := []Message{
		NewUserMsg("hello", 1000),
		NewAssistantMsg(AssistantMessage{
			Content:    []AssistantContent{NewTextContent("hi")},
			Api:        ApiAnthropicMessages,
			Provider:   ProviderAnthropic,
			Model:      "claude-3",
			Usage:      Usage{Input: 10, Output: 20, TotalTokens: 30},
			StopReason: StopReasonStop,
			Timestamp:  2000,
		}),
		NewToolResultMsg(ToolResultMessage{
			ToolCallID: "tc1",
			ToolName:   "read",
			Content:    []ToolResultContent{{Type: "text", Text: "file content"}},
			IsError:    false,
			Timestamp:  3000,
		}),
	}

	for _, msg := range msgs {
		t.Run(msg.Role(), func(t *testing.T) {
			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Role() != msg.Role() {
				t.Errorf("role mismatch: got %q, want %q", got.Role(), msg.Role())
			}
		})
	}
}

func TestModel_SupportsImages(t *testing.T) {
	m := &Model{Input: []string{"text", "image"}}
	if !m.SupportsImages() {
		t.Error("expected SupportsImages=true")
	}
	m2 := &Model{Input: []string{"text"}}
	if m2.SupportsImages() {
		t.Error("expected SupportsImages=false")
	}
}

func TestUsage_Defaults(t *testing.T) {
	u := Usage{}
	if u.Input != 0 || u.Output != 0 || u.Cost.Total != 0 {
		t.Error("expected zero defaults")
	}
}

func TestThinkingBudgets_BudgetForLevel(t *testing.T) {
	v := 1024
	tb := &ThinkingBudgets{Minimal: &v}
	if tb.BudgetForLevel(ThinkingMinimal) != 1024 {
		t.Error("expected 1024 for minimal")
	}
	if tb.BudgetForLevel(ThinkingHigh) != 0 {
		t.Error("expected 0 for unset high")
	}
	// nil receiver
	var nilTB *ThinkingBudgets
	if nilTB.BudgetForLevel(ThinkingMinimal) != 0 {
		t.Error("expected 0 for nil budgets")
	}
}

func TestStopReasonConstants(t *testing.T) {
	reasons := []StopReason{StopReasonStop, StopReasonLength, StopReasonToolUse, StopReasonError, StopReasonAborted}
	expected := []string{"stop", "length", "toolUse", "error", "aborted"}
	for i, r := range reasons {
		if string(r) != expected[i] {
			t.Errorf("StopReason %d: got %q, want %q", i, r, expected[i])
		}
	}
}

func TestAssistantMessageEvent_IsDone(t *testing.T) {
	done := AssistantMessageEvent{Type: EventDone}
	if !done.IsDone() {
		t.Error("expected IsDone for done")
	}
	err := AssistantMessageEvent{Type: EventError}
	if !err.IsDone() {
		t.Error("expected IsDone for error")
	}
	delta := AssistantMessageEvent{Type: EventTextDelta}
	if delta.IsDone() {
		t.Error("expected !IsDone for delta")
	}
}

func TestAssistantMessageEvent_FinalMessage(t *testing.T) {
	msg := &AssistantMessage{Model: "test"}
	done := AssistantMessageEvent{Type: EventDone, Message: msg}
	if done.FinalMessage() != msg {
		t.Error("expected final message for done")
	}
	errEvt := AssistantMessageEvent{Type: EventError, Error: msg}
	if errEvt.FinalMessage() != msg {
		t.Error("expected final message for error")
	}
	delta := AssistantMessageEvent{Type: EventTextDelta}
	if delta.FinalMessage() != nil {
		t.Error("expected nil for non-terminal event")
	}
}

func TestNewUserMsg(t *testing.T) {
	msg := NewUserMsg("hello", 1000)
	if msg.Role() != RoleUser {
		t.Errorf("expected user role, got %q", msg.Role())
	}
	u := msg.AsUser()
	if u == nil {
		t.Fatal("expected non-nil user")
	}
	if u.Content != "hello" {
		t.Errorf("expected 'hello', got %v", u.Content)
	}
	if u.Timestamp != 1000 {
		t.Errorf("expected timestamp 1000, got %d", u.Timestamp)
	}
}

func TestNewAssistantMsg(t *testing.T) {
	msg := NewAssistantMsg(AssistantMessage{Model: "test", StopReason: StopReasonStop})
	if msg.Role() != RoleAssistant {
		t.Errorf("expected assistant role, got %q", msg.Role())
	}
	if msg.AsAssistant().Model != "test" {
		t.Error("expected model 'test'")
	}
}

func TestNewToolResultMsg(t *testing.T) {
	msg := NewToolResultMsg(ToolResultMessage{ToolCallID: "tc1", ToolName: "read"})
	if msg.Role() != RoleToolResult {
		t.Errorf("expected toolResult role, got %q", msg.Role())
	}
	if msg.AsToolResult().ToolCallID != "tc1" {
		t.Error("expected toolCallId 'tc1'")
	}
}

func TestAssistantContentDeepCopyCoversAllFields(t *testing.T) {
	covered := map[string]bool{
		"Text":     true,
		"Thinking": true,
		"ToolCall": true,
		"Server":   true,
	}
	typ := reflect.TypeOf(AssistantContent{})
	if typ.NumField() != len(covered) {
		t.Fatalf("expected %d fields, got %d", len(covered), typ.NumField())
	}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !covered[name] {
			t.Fatalf("AssistantContent field %q not handled in DeepCopy -- update DeepCopy and this test", name)
		}
	}
}
