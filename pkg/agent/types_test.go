package agent

import (
	"testing"

	"github.com/kfet/tau/pkg/ai"
)

func TestToAIThinkingLevel(t *testing.T) {
	tests := []struct {
		level ThinkingLevel
		want  ai.ThinkingLevel
	}{
		{ThinkingOff, ""},
		{ThinkingMinimal, ai.ThinkingMinimal},
		{ThinkingLow, ai.ThinkingLow},
		{ThinkingMedium, ai.ThinkingMedium},
		{ThinkingHigh, ai.ThinkingHigh},
		{ThinkingXHigh, ai.ThinkingXHigh},
	}
	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			got := ToAIThinkingLevel(tt.level)
			if got != tt.want {
				t.Errorf("ToAIThinkingLevel(%s) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

func TestNewAgentMessage(t *testing.T) {
	msg := ai.NewUserMsg("hello", 1000)
	am := NewAgentMessage(msg)
	if am.Role() != "user" {
		t.Errorf("Role() = %q, want user", am.Role())
	}
}

func TestAgentState_Initial(t *testing.T) {
	state := AgentState{
		SystemPrompt:    "You are helpful.",
		ThinkingLevel:   ThinkingMedium,
		Messages:        []AgentMessage{},
		PendingToolCalls: make(map[string]bool),
	}
	if state.SystemPrompt != "You are helpful." {
		t.Error("system prompt wrong")
	}
	if state.IsStreaming {
		t.Error("should not be streaming initially")
	}
	if len(state.PendingToolCalls) != 0 {
		t.Error("should have no pending tool calls initially")
	}
}

func TestAgentToolResult(t *testing.T) {
	result := AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: "file contents here"},
		},
		Details: map[string]any{"lineCount": 42},
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "file contents here" {
		t.Errorf("text = %q", result.Content[0].Text)
	}
}

func TestAgentEventType_Constants(t *testing.T) {
	types := []AgentEventType{
		EventAgentStart, EventAgentEnd,
		EventTurnStart, EventTurnEnd,
		EventMessageStart, EventMessageUpdate, EventMessageEnd,
		EventToolExecutionStart, EventToolExecutionUpdate, EventToolExecutionEnd,
	}
	seen := make(map[AgentEventType]bool)
	for _, typ := range types {
		if typ == "" {
			t.Error("empty event type")
		}
		if seen[typ] {
			t.Errorf("duplicate event type: %s", typ)
		}
		seen[typ] = true
	}
}

func TestAgentEvent_AgentStart(t *testing.T) {
	event := AgentEvent{Type: EventAgentStart}
	if event.Type != EventAgentStart {
		t.Errorf("Type = %s, want agent_start", event.Type)
	}
}

func TestAgentEvent_ToolExecution(t *testing.T) {
	event := AgentEvent{
		Type:       EventToolExecutionStart,
		ToolCallID: "call-1",
		ToolName:   "read",
		Args:       map[string]any{"path": "test.txt"},
	}
	if event.ToolCallID != "call-1" {
		t.Errorf("ToolCallID = %q", event.ToolCallID)
	}
	if event.ToolName != "read" {
		t.Errorf("ToolName = %q", event.ToolName)
	}
}

func TestThinkingLevel_Off(t *testing.T) {
	level := ThinkingOff
	if level != "off" {
		t.Errorf("ThinkingOff = %q, want off", level)
	}
	aiLevel := ToAIThinkingLevel(level)
	if aiLevel != "" {
		t.Errorf("ToAIThinkingLevel(off) = %q, want empty", aiLevel)
	}
}
