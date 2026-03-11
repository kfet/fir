package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/session"
)

func TestSkillInvocationMessage_Collapsed(t *testing.T) {
	block := &session.ParsedSkillBlock{
		Name:    "test-skill",
		Content: "skill content here",
	}
	comp := NewSkillInvocationMessageComponent(block, nil)
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "skill") {
		t.Errorf("expected '[skill]' label, got %q", joined)
	}
	if !strings.Contains(joined, "test-skill") {
		t.Errorf("expected skill name, got %q", joined)
	}
	if !strings.Contains(joined, "expand") {
		t.Errorf("expected expand hint, got %q", joined)
	}
}

func TestSkillInvocationMessage_Expanded(t *testing.T) {
	block := &session.ParsedSkillBlock{
		Name:    "test-skill",
		Content: "skill content here",
	}
	comp := NewSkillInvocationMessageComponent(block, nil)
	comp.SetExpanded(true)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "skill") {
		t.Errorf("expected '[skill]' label, got %q", joined)
	}
	if !strings.Contains(joined, "test-skill") {
		t.Errorf("expected skill name in header, got %q", joined)
	}
	if !strings.Contains(joined, "skill content here") {
		t.Errorf("expected skill content, got %q", joined)
	}
}
