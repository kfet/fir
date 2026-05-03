package extension

import (
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestMessageEndPayload_Nil(t *testing.T) {
	if got := messageEndPayload(nil); got != nil {
		t.Fatalf("nil event: want nil payload, got %v", got)
	}
	ev := &agent.AgentEvent{}
	if got := messageEndPayload(ev); got != nil {
		t.Fatalf("event with nil Message: want nil payload, got %v", got)
	}
}

func TestMessageEndPayload_Assistant(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Role:       "assistant",
		Provider:   "anthropic",
		Model:      "claude-3-5-sonnet",
		StopReason: ai.StopReasonStop,
		ResponseID: "resp_123",
		Usage: ai.Usage{
			Input:       100,
			Output:      50,
			CacheRead:   20,
			CacheWrite:  10,
			TotalTokens: 180,
			Cost: ai.UsageCost{
				Input:      0.001,
				Output:     0.002,
				CacheRead:  0.0001,
				CacheWrite: 0.0002,
				Total:      0.0033,
			},
		},
	})
	am := agent.NewAgentMessage(msg)
	p := messageEndPayload(&agent.AgentEvent{Type: agent.EventMessageEnd, Message: &am})
	if p == nil {
		t.Fatal("want non-nil payload")
	}
	if p.Role != "assistant" {
		t.Errorf("role=%v", p.Role)
	}
	if p.Provider != "anthropic" {
		t.Errorf("provider=%v", p.Provider)
	}
	if p.Model != "claude-3-5-sonnet" {
		t.Errorf("model=%v", p.Model)
	}
	if p.ResponseID != "resp_123" {
		t.Errorf("response_id=%v", p.ResponseID)
	}
	u := p.Usage
	if u == nil {
		t.Fatal("usage nil")
	}
	if u.Input != 100 || u.Output != 50 || u.TotalTokens != 180 {
		t.Errorf("usage tokens wrong: %+v", u)
	}
	if u.Cost.Total != 0.0033 {
		t.Errorf("cost total: %v", u.Cost.Total)
	}
}

func TestMessageEndPayload_User(t *testing.T) {
	msg := ai.NewUserMsg("hi", 0)
	am := agent.NewAgentMessage(msg)
	p := messageEndPayload(&agent.AgentEvent{Type: agent.EventMessageEnd, Message: &am})
	if p == nil || p.Role != "user" {
		t.Fatalf("user payload: %v", p)
	}
	if p.Usage != nil {
		t.Errorf("user payload should not include usage: %+v", p)
	}
}
