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
	if p["role"] != "assistant" {
		t.Errorf("role=%v", p["role"])
	}
	if p["provider"] != "anthropic" {
		t.Errorf("provider=%v", p["provider"])
	}
	if p["model"] != "claude-3-5-sonnet" {
		t.Errorf("model=%v", p["model"])
	}
	if p["response_id"] != "resp_123" {
		t.Errorf("response_id=%v", p["response_id"])
	}
	u, ok := p["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage not a map: %T", p["usage"])
	}
	if u["input"].(int) != 100 || u["output"].(int) != 50 || u["total_tokens"].(int) != 180 {
		t.Errorf("usage tokens wrong: %v", u)
	}
	cost, ok := u["cost"].(map[string]any)
	if !ok {
		t.Fatalf("cost not a map: %T", u["cost"])
	}
	if cost["total"].(float64) != 0.0033 {
		t.Errorf("cost total: %v", cost["total"])
	}
}

func TestMessageEndPayload_User(t *testing.T) {
	msg := ai.NewUserMsg("hi", 0)
	am := agent.NewAgentMessage(msg)
	p := messageEndPayload(&agent.AgentEvent{Type: agent.EventMessageEnd, Message: &am})
	if p == nil || p["role"] != "user" {
		t.Fatalf("user payload: %v", p)
	}
	if _, ok := p["usage"]; ok {
		t.Errorf("user payload should not include usage: %v", p)
	}
}
