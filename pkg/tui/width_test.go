package tui

import (
	"strings"
	"testing"
)

// --- VisibleWidth ---

func TestVisibleWidth_Empty(t *testing.T) {
	if w := VisibleWidth(""); w != 0 {
		t.Errorf("expected 0, got %d", w)
	}
}

func TestVisibleWidth_ASCII(t *testing.T) {
	if w := VisibleWidth("hello"); w != 5 {
		t.Errorf("expected 5, got %d", w)
	}
}

func TestVisibleWidth_Tab(t *testing.T) {
	if w := VisibleWidth("a\tb"); w != 5 {
		t.Errorf("expected 5 (a + 3 spaces + b), got %d", w)
	}
}

func TestVisibleWidth_ANSI_SGR(t *testing.T) {
	s := "\x1b[31mred\x1b[0m"
	if w := VisibleWidth(s); w != 3 {
		t.Errorf("expected 3 (red), got %d", w)
	}
}

func TestVisibleWidth_ANSI_256Color(t *testing.T) {
	s := "\x1b[38;5;240mtext\x1b[0m"
	if w := VisibleWidth(s); w != 4 {
		t.Errorf("expected 4 (text), got %d", w)
	}
}

func TestVisibleWidth_OSC8_Hyperlink(t *testing.T) {
	s := "\x1b]8;;https://example.com\x07link\x1b]8;;\x07"
	if w := VisibleWidth(s); w != 4 {
		t.Errorf("expected 4 (link), got %d", w)
	}
}

func TestVisibleWidth_Cached(t *testing.T) {
	s := "\x1b[1mbold\x1b[0m"
	w1 := VisibleWidth(s)
	w2 := VisibleWidth(s)
	if w1 != w2 {
		t.Errorf("cached width mismatch: %d vs %d", w1, w2)
	}
	if w1 != 4 {
		t.Errorf("expected 4, got %d", w1)
	}
}

// --- ApplyBackgroundToLine ---

func TestApplyBackgroundToLine(t *testing.T) {
	bg := func(s string) string { return "[" + s + "]" }
	result := ApplyBackgroundToLine("hi", 10, bg)
	if result != "[hi        ]" {
		t.Errorf("expected [hi        ], got %q", result)
	}
}

// --- TruncateToWidth ---

func TestTruncateToWidth_Short(t *testing.T) {
	result := TruncateToWidth("hi", 10, "...", false)
	if result != "hi" {
		t.Errorf("expected 'hi', got %q", result)
	}
}

func TestTruncateToWidth_Truncated(t *testing.T) {
	result := TruncateToWidth("hello world", 8, "...", false)
	if VisibleWidth(result) > 8 {
		t.Errorf("truncated result too wide: %d", VisibleWidth(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Errorf("expected ellipsis suffix, got %q", result)
	}
}

func TestTruncateToWidth_Padded(t *testing.T) {
	result := TruncateToWidth("hi", 10, "...", true)
	if VisibleWidth(result) != 10 {
		t.Errorf("expected width 10, got %d", VisibleWidth(result))
	}
}

// --- SliceByColumn ---

func TestSliceByColumn_Simple(t *testing.T) {
	result := SliceByColumn("hello world", 6, 5, false)
	if result != "world" {
		t.Errorf("expected 'world', got %q", result)
	}
}

func TestSliceByColumn_WithAnsi(t *testing.T) {
	s := "\x1b[31mhello\x1b[0m world"
	result := SliceByColumn(s, 0, 5, false)
	if !strings.Contains(result, "\x1b[31m") {
		t.Errorf("expected ANSI in slice, got %q", result)
	}
	if VisibleWidth(result) != 5 {
		t.Errorf("expected visible width 5, got %d", VisibleWidth(result))
	}
}

func TestSliceByColumn_ZeroLength(t *testing.T) {
	result := SliceByColumn("hello", 0, 0, false)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

// --- Helpers ---

func TestIsWhitespaceChar(t *testing.T) {
	if !IsWhitespaceChar(' ') {
		t.Error("space should be whitespace")
	}
	if IsWhitespaceChar('a') {
		t.Error("'a' should not be whitespace")
	}
}

func TestIsPunctuationChar(t *testing.T) {
	if !IsPunctuationChar('.') {
		t.Error("'.' should be punctuation")
	}
	if IsPunctuationChar('a') {
		t.Error("'a' should not be punctuation")
	}
}
