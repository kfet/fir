package components

import (
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/tui"
)

func TestBox_Empty(t *testing.T) {
	b := NewBox(1, 1, nil)
	lines := b.Render(80)
	if lines != nil {
		t.Errorf("expected nil for empty box, got %v", lines)
	}
}

func TestBox_SingleChild(t *testing.T) {
	b := NewBox(0, 0, nil)
	b.AddChild(NewSpacer(1))
	lines := b.Render(80)
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
}

func TestBox_WithPadding(t *testing.T) {
	b := NewBox(2, 1, nil)
	txt := NewText("hello", 0, 0, nil)
	b.AddChild(txt)
	lines := b.Render(80)
	// paddingY=1 → 1 top + content + 1 bottom
	if len(lines) < 3 {
		t.Fatalf("expected >= 3 lines with paddingY=1, got %d", len(lines))
	}
	// Content should have left padding
	for _, line := range lines[1 : len(lines)-1] {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("expected left padding, got %q", line)
		}
	}
}

func TestBox_WithBackground(t *testing.T) {
	bg := func(s string) string { return "[" + s + "]" }
	b := NewBox(0, 0, bg)
	b.AddChild(NewSpacer(1))
	lines := b.Render(10)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "[") {
		t.Errorf("expected background, got %q", lines[0])
	}
}

func TestBox_RemoveChild(t *testing.T) {
	b := NewBox(0, 0, nil)
	s1 := NewSpacer(1)
	s2 := NewSpacer(1)
	b.AddChild(s1)
	b.AddChild(s2)
	b.RemoveChild(s1)
	if len(b.Children) != 1 {
		t.Errorf("expected 1 child after remove, got %d", len(b.Children))
	}
}

func TestBox_Clear(t *testing.T) {
	b := NewBox(0, 0, nil)
	b.AddChild(NewSpacer(1))
	b.AddChild(NewSpacer(1))
	b.Clear()
	if len(b.Children) != 0 {
		t.Errorf("expected 0 children after clear, got %d", len(b.Children))
	}
}

func TestBox_PaddedWidth(t *testing.T) {
	b := NewBox(0, 0, nil)
	txt := NewText("hi", 0, 0, nil)
	b.AddChild(txt)
	lines := b.Render(20)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if tui.VisibleWidth(lines[0]) != 20 {
		t.Errorf("expected padded to width 20, got %d", tui.VisibleWidth(lines[0]))
	}
}

func TestBox_Invalidate(t *testing.T) {
	b := NewBox(0, 0, nil)
	b.AddChild(NewSpacer(1))
	_ = b.Render(80)
	b.Invalidate() // should not panic
	lines := b.Render(80)
	if len(lines) != 1 {
		t.Errorf("expected 1 line after invalidate, got %d", len(lines))
	}
}

func TestBox_Cache(t *testing.T) {
	b := NewBox(0, 0, nil)
	b.AddChild(NewSpacer(1))
	lines1 := b.Render(80)
	lines2 := b.Render(80)
	// Should return cached result
	if len(lines1) != len(lines2) {
		t.Errorf("cache mismatch: %d vs %d", len(lines1), len(lines2))
	}
}
