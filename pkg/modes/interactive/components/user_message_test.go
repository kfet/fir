package components

import (
	"strings"
	"testing"
)

func TestUserMessageComponent_Render(t *testing.T) {
	comp := NewUserMessageComponent("Hello world", nil)
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected at least one rendered line")
	}
	// First line should be a spacer (empty line)
	// Remaining lines should contain the text
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Hello world") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected rendered output to contain 'Hello world', got %v", lines)
	}
}

func TestUserMessageComponent_EmptyText(t *testing.T) {
	comp := NewUserMessageComponent("", nil)
	lines := comp.Render(80)
	// Should still render (at least the spacer)
	if len(lines) == 0 {
		t.Fatal("expected at least one rendered line for empty text")
	}
}
