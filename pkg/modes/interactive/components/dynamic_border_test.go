package components

import (
	"strings"
	"testing"
)

func TestDynamicBorder_Render(t *testing.T) {
	db := NewDynamicBorder(func(s string) string { return s })
	lines := db.Render(10)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != strings.Repeat("─", 10) {
		t.Errorf("expected 10 dashes, got %q", lines[0])
	}
}

func TestDynamicBorder_Render_MinWidth(t *testing.T) {
	db := NewDynamicBorder(func(s string) string { return s })
	lines := db.Render(0)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != "─" {
		t.Errorf("expected 1 dash for zero width, got %q", lines[0])
	}
}

func TestDynamicBorder_Render_WithColor(t *testing.T) {
	db := NewDynamicBorder(func(s string) string { return "[" + s + "]" })
	lines := db.Render(3)
	expected := "[" + strings.Repeat("─", 3) + "]"
	if lines[0] != expected {
		t.Errorf("expected %q, got %q", expected, lines[0])
	}
}

func TestDynamicBorder_Invalidate(t *testing.T) {
	db := NewDynamicBorder(nil)
	db.Invalidate() // should not panic
}
