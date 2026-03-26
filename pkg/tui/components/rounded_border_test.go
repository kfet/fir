package components

import (
	"strings"
	"testing"
)

func TestRoundedBorder_Render(t *testing.T) {
	border := NewRoundedBorder(nil, 1)
	border.AddChild(NewText("hello", 0, 0, nil))

	lines := border.Render(20)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[0], "╮") {
		t.Errorf("top border missing corners: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "╰") || !strings.Contains(lines[len(lines)-1], "╯") {
		t.Errorf("bottom border missing corners: %q", lines[len(lines)-1])
	}
	// Middle line should contain the text
	found := false
	for _, l := range lines[1 : len(lines)-1] {
		if strings.Contains(l, "hello") {
			found = true
		}
	}
	if !found {
		t.Error("content 'hello' not found in bordered output")
	}
}

func TestIndented_Render(t *testing.T) {
	inner := NewText("test", 0, 0, nil)
	ind := NewIndented(inner, 3)
	lines := ind.Render(40)
	for _, l := range lines {
		if !strings.HasPrefix(l, "   ") {
			t.Errorf("expected 3-space indent, got: %q", l)
		}
	}
}

func TestIndented_EmptyLines(t *testing.T) {
	inner := NewText("line1\n\nline2", 0, 0, nil)
	ind := NewIndented(inner, 2)
	lines := ind.Render(40)
	for _, l := range lines {
		if l == "" {
			continue // empty lines should remain empty
		}
		if !strings.HasPrefix(l, "  ") {
			t.Errorf("expected 2-space indent on non-empty line, got: %q", l)
		}
	}
}

func TestRoundedBorder_Empty(t *testing.T) {
	border := NewRoundedBorder(nil, 0)
	lines := border.Render(10)
	if len(lines) != 2 {
		t.Errorf("empty border should have 2 lines (top+bottom), got %d", len(lines))
	}
}

func TestRoundedBorder_WithColorFn(t *testing.T) {
	called := false
	border := NewRoundedBorder(func(s string) string {
		called = true
		return "[" + s + "]"
	}, 0)
	border.AddChild(NewText("x", 0, 0, nil))
	lines := border.Render(10)
	if !called {
		t.Error("colorFn was not called")
	}
	if !strings.Contains(lines[0], "[╭") {
		t.Errorf("expected colored top border, got: %q", lines[0])
	}
}
