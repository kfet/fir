package components

import (
	"strings"
	"testing"
)

func TestExtensionSelectorComponent_Render(t *testing.T) {
	options := []string{"Option A", "Option B", "Option C"}
	comp := NewExtensionSelectorComponent("Pick one", options, func(string) {}, func() {}, nil)

	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "Pick one") {
		t.Error("expected title in output")
	}
	if !strings.Contains(output, "Option A") {
		t.Error("expected 'Option A' in output")
	}
}

func TestExtensionSelectorComponent_Navigation(t *testing.T) {
	options := []string{"A", "B", "C"}
	var selected string
	comp := NewExtensionSelectorComponent("Pick", options, func(opt string) { selected = opt }, func() {}, nil)

	// Initial selection is 0
	comp.HandleInput("\x1b[B") // down
	comp.HandleInput("\r")     // enter

	if selected != "B" {
		t.Errorf("selected = %q, want %q", selected, "B")
	}
}

func TestExtensionSelectorComponent_Cancel(t *testing.T) {
	cancelled := false
	comp := NewExtensionSelectorComponent("Pick", []string{"A"}, func(string) {}, func() { cancelled = true }, nil)

	comp.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel callback")
	}
}

func TestExtensionSelectorComponent_VimKeys(t *testing.T) {
	options := []string{"First", "Second", "Third"}
	var selected string
	comp := NewExtensionSelectorComponent("Pick", options, func(opt string) { selected = opt }, func() {}, nil)

	comp.HandleInput("j") // vim down
	comp.HandleInput("j") // vim down
	comp.HandleInput("\r") // enter

	if selected != "Third" {
		t.Errorf("selected = %q, want %q", selected, "Third")
	}
}

func TestExtensionSelectorComponent_BoundsClamping(t *testing.T) {
	options := []string{"Only"}
	var selected string
	comp := NewExtensionSelectorComponent("Pick", options, func(opt string) { selected = opt }, func() {}, nil)

	// Up on first item should not move
	comp.HandleInput("k")
	comp.HandleInput("\r")

	if selected != "Only" {
		t.Errorf("selected = %q, want %q", selected, "Only")
	}
}
