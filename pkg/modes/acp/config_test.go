package acp

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func TestBuildThinkingConfigOption_WithLevels(t *testing.T) {
	// Create a mock session that returns reasoning levels.
	sess := &mockConfigSession{
		thinkingLevel: string(agent.ThinkingMedium),
		availableLevels: []agent.ThinkingLevel{
			agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh,
		},
	}

	opt := buildThinkingConfigOptionFromAccessor(sess)

	if opt.Id != thinkingConfigID {
		t.Errorf("Id = %q, want %q", opt.Id, thinkingConfigID)
	}
	if opt.Type != "select" {
		t.Errorf("Type = %q, want select", opt.Type)
	}
	if opt.Category != SessionConfigCategoryThoughtLevel {
		t.Errorf("Category = %q, want thought_level", opt.Category)
	}
	if opt.CurrentValue != string(agent.ThinkingMedium) {
		t.Errorf("CurrentValue = %q, want medium", opt.CurrentValue)
	}
	if len(opt.Options) != 4 {
		t.Fatalf("expected 4 options, got %d", len(opt.Options))
	}
	if opt.Options[0].Value != string(agent.ThinkingOff) {
		t.Errorf("first option = %q, want off", opt.Options[0].Value)
	}
}

func TestBuildThinkingConfigOption_NonReasoning(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel:   string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff},
	}

	opt := buildThinkingConfigOptionFromAccessor(sess)

	if len(opt.Options) != 1 {
		t.Errorf("expected 1 option for non-reasoning, got %d", len(opt.Options))
	}
	if opt.CurrentValue != string(agent.ThinkingOff) {
		t.Errorf("CurrentValue = %q, want off", opt.CurrentValue)
	}
}

func TestBuildThinkingConfigOption_EmptyLevel(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel:   "", // empty defaults to "off"
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingMedium},
	}

	opt := buildThinkingConfigOptionFromAccessor(sess)

	if opt.CurrentValue != string(agent.ThinkingOff) {
		t.Errorf("CurrentValue = %q, want off (default)", opt.CurrentValue)
	}
}

func TestSetSessionConfigOption_ThinkingLevel(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel: string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{
			agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh,
		},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{
		sessions: map[string]*firSession{"s1": entry},
	}

	resp, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingHigh),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.ConfigOptions) == 0 {
		t.Fatal("expected config options in response")
	}
	found := false
	for _, opt := range resp.ConfigOptions {
		if opt.Id == thinkingConfigID {
			if opt.CurrentValue != string(agent.ThinkingHigh) {
				t.Errorf("currentValue = %q, want high", opt.CurrentValue)
			}
			found = true
		}
	}
	if !found {
		t.Error("thinking_level config not found in response")
	}
	if sess.thinkingLevel != string(agent.ThinkingHigh) {
		t.Errorf("session thinking level = %q, want high", sess.thinkingLevel)
	}
}

func TestSetSessionConfigOption_InvalidLevel(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel:   string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{
		sessions: map[string]*firSession{"s1": entry},
	}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingHigh),
	})
	if err == nil {
		t.Error("expected error for invalid thinking level")
	}
}

func TestSetSessionConfigOption_UnknownConfig(t *testing.T) {
	sess := &mockConfigSession{}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{
		sessions: map[string]*firSession{"s1": entry},
	}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  "nonexistent",
		Value:     "whatever",
	})
	if err == nil {
		t.Error("expected error for unknown config")
	}
}

func TestSetSessionConfigOption_UnknownSession(t *testing.T) {
	pa := &firAgent{
		sessions: map[string]*firSession{},
	}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "nonexistent",
		ConfigId:  thinkingConfigID,
		Value:     "off",
	})
	if err == nil {
		t.Error("expected error for unknown session")
	}
}

// mockConfigSession implements thinkingAccessor for testing.
type mockConfigSession struct {
	thinkingLevel   string
	availableLevels []agent.ThinkingLevel
}

func (m *mockConfigSession) ThinkingLevel() string {
	return m.thinkingLevel
}

func (m *mockConfigSession) GetAvailableThinkingLevels() []agent.ThinkingLevel {
	return m.availableLevels
}

func (m *mockConfigSession) SetThinkingLevel(level string) {
	m.thinkingLevel = level
}
