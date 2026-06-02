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

func TestProviderErrorPayload_Nil(t *testing.T) {
	if got := providerErrorPayload(nil); got != nil {
		t.Fatalf("nil event: want nil, got %v", got)
	}
	if got := providerErrorPayload(&agent.AgentEvent{}); got != nil {
		t.Fatalf("event with nil TurnMessage: want nil, got %v", got)
	}
}

func TestProviderErrorPayload_NonErrorStop(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Role: "assistant", Provider: "anthropic", Model: "m", StopReason: ai.StopReasonStop,
	})
	am := agent.NewAgentMessage(msg)
	if got := providerErrorPayload(&agent.AgentEvent{Type: agent.EventTurnEnd, TurnMessage: &am}); got != nil {
		t.Fatalf("non-error stop: want nil, got %v", got)
	}
}

func TestProviderErrorPayload_Classifies(t *testing.T) {
	cases := []struct {
		name      string
		errText   string
		wantKind  string
		retryable bool
	}{
		{"overloaded", "Overloaded (overloaded_error)", "overloaded", true},
		{"ratelimit", "429 Too Many Requests rate limit exceeded", "rate_limit", true},
		{"server", "503 Service Unavailable", "server", true},
		{"transport", "connection reset by peer", "transport", true},
		{"terminal", "400 Bad Request: context length exceeded", "terminal", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := ai.NewAssistantMsg(ai.AssistantMessage{
				Role: "assistant", Provider: "anthropic", Model: "claude",
				StopReason: ai.StopReasonError, ErrorMessage: tc.errText,
			})
			am := agent.NewAgentMessage(msg)
			p := providerErrorPayload(&agent.AgentEvent{Type: agent.EventTurnEnd, TurnMessage: &am})
			if p == nil {
				t.Fatal("want non-nil payload")
			}
			if p.Kind != tc.wantKind {
				t.Errorf("kind=%q want %q", p.Kind, tc.wantKind)
			}
			if p.Retryable != tc.retryable {
				t.Errorf("retryable=%v want %v", p.Retryable, tc.retryable)
			}
			if p.ErrorText != tc.errText {
				t.Errorf("error_text=%q", p.ErrorText)
			}
			if p.Provider != "anthropic" || p.Model != "claude" {
				t.Errorf("provider/model: %q/%q", p.Provider, p.Model)
			}
		})
	}
}

func TestProviderErrorPayload_RetryAfter(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Role: "assistant", StopReason: ai.StopReasonError,
		ErrorMessage: "rate limit exceeded. Please retry in 30s",
	})
	am := agent.NewAgentMessage(msg)
	p := providerErrorPayload(&agent.AgentEvent{Type: agent.EventTurnEnd, TurnMessage: &am})
	if p == nil || p.RetryAfterMs != 30000 {
		t.Fatalf("retry_after_ms: %v", p)
	}
}
