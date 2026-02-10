package components

import (
	"strings"
	"testing"
)

func TestText_EmptyText(t *testing.T) {
	txt := NewText("", 1, 1, nil)
	lines := txt.Render(80)
	if len(lines) != 0 {
		t.Errorf("expected no lines for empty text, got %d", len(lines))
	}
}

func TestText_WhitespaceOnly(t *testing.T) {
	txt := NewText("   ", 1, 1, nil)
	lines := txt.Render(80)
	if len(lines) != 0 {
		t.Errorf("expected no lines for whitespace-only text, got %d", len(lines))
	}
}

func TestText_SimpleText(t *testing.T) {
	txt := NewText("Hello", 1, 0, nil)
	lines := txt.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "Hello") {
		t.Errorf("expected line to contain 'Hello', got %q", lines[0])
	}
}

func TestText_PaddingY(t *testing.T) {
	txt := NewText("Hello", 0, 2, nil)
	lines := txt.Render(80)
	// 2 top + 1 content + 2 bottom = 5
	if len(lines) != 5 {
		t.Errorf("expected 5 lines (2 top pad + 1 content + 2 bottom pad), got %d", len(lines))
	}
}

func TestText_PaddingX(t *testing.T) {
	txt := NewText("Hi", 3, 0, nil)
	lines := txt.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	// Should start with 3 spaces (left margin)
	if !strings.HasPrefix(lines[0], "   Hi") {
		t.Errorf("expected left padding, got %q", lines[0])
	}
}

func TestText_Wrapping(t *testing.T) {
	txt := NewText("hello world this is a long text", 0, 0, nil)
	lines := txt.Render(12)
	if len(lines) < 2 {
		t.Errorf("expected wrapping to produce multiple lines at width 12, got %d", len(lines))
	}
}

func TestText_TabReplacement(t *testing.T) {
	txt := NewText("a\tb", 0, 0, nil)
	lines := txt.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "a   b") {
		t.Errorf("expected tab replaced with 3 spaces, got %q", lines[0])
	}
}

func TestText_SetText(t *testing.T) {
	txt := NewText("old", 0, 0, nil)
	lines1 := txt.Render(80)
	txt.SetText("new")
	lines2 := txt.Render(80)
	if strings.TrimSpace(lines1[0]) == strings.TrimSpace(lines2[0]) {
		t.Error("expected different output after SetText")
	}
}

func TestText_Cache(t *testing.T) {
	txt := NewText("cached", 0, 0, nil)
	lines1 := txt.Render(80)
	lines2 := txt.Render(80) // Should hit cache
	if len(lines1) != len(lines2) {
		t.Error("cache should return same lines")
	}
}

func TestText_Invalidate(t *testing.T) {
	txt := NewText("test", 0, 0, nil)
	txt.Render(80)
	txt.Invalidate()
	// Should not panic
	lines := txt.Render(80)
	if len(lines) == 0 {
		t.Error("expected lines after invalidate")
	}
}

func TestText_CustomBgFn(t *testing.T) {
	bgFn := func(s string) string {
		return "\x1b[44m" + s + "\x1b[0m"
	}
	txt := NewText("Hi", 0, 0, bgFn)
	lines := txt.Render(10)
	if len(lines) == 0 {
		t.Fatal("expected lines")
	}
	if !strings.Contains(lines[0], "\x1b[44m") {
		t.Errorf("expected background escape, got %q", lines[0])
	}
}

func TestText_WidthPadding(t *testing.T) {
	txt := NewText("Hi", 0, 0, nil)
	lines := txt.Render(20)
	if len(lines[0]) != 20 {
		t.Errorf("expected line padded to width 20, got %d: %q", len(lines[0]), lines[0])
	}
}
