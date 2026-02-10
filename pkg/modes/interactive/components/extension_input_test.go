package components

import (
	"strings"
	"testing"
)

func TestExtensionInputComponent_Render(t *testing.T) {
	comp := NewExtensionInputComponent("Enter value", "", func(string) {}, func() {}, nil)

	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "Enter value") {
		t.Error("expected title in output")
	}
}

func TestExtensionInputComponent_Submit(t *testing.T) {
	var submitted string
	comp := NewExtensionInputComponent("Title", "", func(val string) { submitted = val }, func() {}, nil)

	// Type something, then submit
	comp.HandleInput("hello")
	comp.HandleInput("\r") // enter

	if submitted != "hello" {
		t.Errorf("submitted = %q, want %q", submitted, "hello")
	}
}

func TestExtensionInputComponent_Cancel(t *testing.T) {
	cancelled := false
	comp := NewExtensionInputComponent("Title", "", func(string) {}, func() { cancelled = true }, nil)

	comp.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel callback")
	}
}
