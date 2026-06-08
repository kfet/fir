package acp

import (
	"context"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/models"
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

func TestSetSessionConfigOption_ClampsUnsupportedLevel(t *testing.T) {
	// Model only supports off/low/medium/high. Requesting "max" should
	// clamp down the canonical ladder to "high".
	sess := &mockConfigSession{
		thinkingLevel: string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{
			agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh,
		},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	resp, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingMax),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.thinkingLevel != string(agent.ThinkingHigh) {
		t.Errorf("thinking level = %q, want %q (clamped from max)", sess.thinkingLevel, agent.ThinkingHigh)
	}
	// The returned config option should reflect the clamped value.
	for _, opt := range resp.ConfigOptions {
		if opt.Id == thinkingConfigID && opt.CurrentValue != string(agent.ThinkingHigh) {
			t.Errorf("response CurrentValue = %q, want high", opt.CurrentValue)
		}
	}
}

func TestSetSessionConfigOption_ClampsToOffOnNonReasoning(t *testing.T) {
	// Non-reasoning model only supports "off". Any request clamps to off.
	sess := &mockConfigSession{
		thinkingLevel:   string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingHigh),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.thinkingLevel != string(agent.ThinkingOff) {
		t.Errorf("thinking level = %q, want off", sess.thinkingLevel)
	}
}

func TestSetSessionConfigOption_ClampsXHighToHighWhenUnsupported(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel: string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{
			agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow,
			agent.ThinkingMedium, agent.ThinkingHigh,
		},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingXHigh),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.thinkingLevel != string(agent.ThinkingHigh) {
		t.Errorf("thinking level = %q, want high (clamped from xhigh)", sess.thinkingLevel)
	}
}

func TestSetSessionConfigOption_PassesThroughSupportedLevel(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel: string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{
			agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium,
			agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax,
		},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     string(agent.ThinkingMax),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.thinkingLevel != string(agent.ThinkingMax) {
		t.Errorf("thinking level = %q, want max", sess.thinkingLevel)
	}
}

func TestSetSessionConfigOption_RejectsEmptyLevel(t *testing.T) {
	sess := &mockConfigSession{
		thinkingLevel:   string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     "",
	})
	if err == nil {
		t.Error("expected error for empty thinking level")
	}
}

func TestSetSessionConfigOption_RejectsGarbageLevel(t *testing.T) {
	// Values that aren't real thinking levels must be rejected, not silently
	// coerced to off.
	sess := &mockConfigSession{
		thinkingLevel:   string(agent.ThinkingOff),
		availableLevels: []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh},
	}
	entry := &firSession{configAccessor: sess}
	pa := &firAgent{sessions: map[string]*firSession{"s1": entry}}

	_, err := pa.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{
		SessionId: "s1",
		ConfigId:  thinkingConfigID,
		Value:     "bogus",
	})
	if err == nil {
		t.Error("expected error for garbage thinking level")
	}
	if sess.thinkingLevel != string(agent.ThinkingOff) {
		t.Errorf("session level mutated to %q on rejection", sess.thinkingLevel)
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

func TestBuildModelConfigOption(t *testing.T) {
	auth := auth.NewInMemoryAuthStorage(nil)
	auth.SetRuntimeApiKey("anthropic", "test-key")
	reg := models.NewModelRegistry(auth, "")

	available := reg.GetAvailable()
	if len(available) == 0 {
		t.Skip("no available models with auth")
	}

	current := available[0]
	opt := buildModelConfigOption(reg, current)

	if opt.Id != modelConfigID {
		t.Errorf("Id = %q, want %q", opt.Id, modelConfigID)
	}
	if opt.Type != "select" {
		t.Errorf("Type = %q, want select", opt.Type)
	}
	if opt.Category != SessionConfigCategoryModel {
		t.Errorf("Category = %q, want model", opt.Category)
	}
	if len(opt.Options) == 0 {
		t.Error("expected available model options")
	}
	if opt.CurrentValue == "" {
		t.Error("expected non-empty currentValue")
	}
	// Current value should be in options.
	found := false
	for _, o := range opt.Options {
		if o.Value == opt.CurrentValue {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("currentValue %q not found in options", opt.CurrentValue)
	}
}

func TestBuildModelConfigOption_NilModel(t *testing.T) {
	auth := auth.NewInMemoryAuthStorage(nil)
	reg := models.NewModelRegistry(auth, "")

	opt := buildModelConfigOption(reg, nil)
	if opt.CurrentValue != "" {
		t.Errorf("expected empty currentValue for nil model, got %q", opt.CurrentValue)
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
