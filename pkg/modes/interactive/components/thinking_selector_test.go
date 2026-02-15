package components

import (
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/agent"
)

func TestThinkingSelectorComponent_Render(t *testing.T) {
	levels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
		agent.ThinkingXHigh,
	}
	comp := NewThinkingSelectorComponent(agent.ThinkingMedium, levels, func(agent.ThinkingLevel) {}, func() {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	// Should contain some level labels
	if !strings.Contains(joined, "medium") {
		t.Errorf("expected 'medium' in output, got %q", joined)
	}
}

func TestThinkingSelectorComponent_GetSelectList(t *testing.T) {
	levels := []agent.ThinkingLevel{agent.ThinkingOff, agent.ThinkingHigh}
	comp := NewThinkingSelectorComponent(agent.ThinkingOff, levels, func(agent.ThinkingLevel) {}, func() {})
	sl := comp.GetSelectList()
	if sl == nil {
		t.Fatal("expected non-nil SelectList")
	}
}
