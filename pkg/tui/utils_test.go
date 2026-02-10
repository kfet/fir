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
	// Tab becomes 3 spaces
	if w := VisibleWidth("a\tb"); w != 5 {
		t.Errorf("expected 5 (a + 3 spaces + b), got %d", w)
	}
}

func TestVisibleWidth_ANSI_SGR(t *testing.T) {
	// ANSI codes should not count toward width
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

// --- ExtractAnsiCode ---

func TestExtractAnsiCode_SGR(t *testing.T) {
	s := "\x1b[31m"
	code, length := ExtractAnsiCode(s, 0)
	if code != "\x1b[31m" || length != 5 {
		t.Errorf("expected (\\x1b[31m, 5), got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_NoCode(t *testing.T) {
	code, length := ExtractAnsiCode("hello", 0)
	if code != "" || length != 0 {
		t.Errorf("expected empty, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_OSC(t *testing.T) {
	s := "\x1b]8;;url\x07"
	code, length := ExtractAnsiCode(s, 0)
	if code != s || length != len(s) {
		t.Errorf("expected full OSC, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_APC(t *testing.T) {
	s := "\x1b_data\x07"
	code, length := ExtractAnsiCode(s, 0)
	if code != s || length != len(s) {
		t.Errorf("expected full APC, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_MidString(t *testing.T) {
	s := "abc\x1b[1mdef"
	code, length := ExtractAnsiCode(s, 3)
	if code != "\x1b[1m" || length != 4 {
		t.Errorf("expected (\\x1b[1m, 4), got (%q, %d)", code, length)
	}
}

// --- AnsiCodeTracker ---

func TestAnsiCodeTracker_Bold(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")
	if !tr.HasActiveCodes() {
		t.Error("expected active codes")
	}
	if codes := tr.GetActiveCodes(); codes != "\x1b[1m" {
		t.Errorf("expected bold code, got %q", codes)
	}
}

func TestAnsiCodeTracker_Reset(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")
	tr.Process("\x1b[0m")
	if tr.HasActiveCodes() {
		t.Error("expected no active codes after reset")
	}
}

func TestAnsiCodeTracker_FgColor(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[31m")
	codes := tr.GetActiveCodes()
	if codes != "\x1b[31m" {
		t.Errorf("expected fg color, got %q", codes)
	}
}

func TestAnsiCodeTracker_256Color(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[38;5;240m")
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "38;5;240") {
		t.Errorf("expected 256-color code, got %q", codes)
	}
}

func TestAnsiCodeTracker_RGB(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[38;2;255;128;0m")
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "38;2;255;128;0") {
		t.Errorf("expected RGB code, got %q", codes)
	}
}

func TestAnsiCodeTracker_Multiple(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")  // bold
	tr.Process("\x1b[31m") // red
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "1") || !strings.Contains(codes, "31") {
		t.Errorf("expected bold+red, got %q", codes)
	}
}

func TestAnsiCodeTracker_LineEndReset(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[4m") // underline
	r := tr.GetLineEndReset()
	if r != "\x1b[24m" {
		t.Errorf("expected underline off, got %q", r)
	}

	tr.Reset()
	tr.Process("\x1b[1m") // bold only
	r = tr.GetLineEndReset()
	if r != "" {
		t.Errorf("expected empty for bold, got %q", r)
	}
}

// --- WrapTextWithAnsi ---

func TestWrapText_ShortLine(t *testing.T) {
	lines := WrapTextWithAnsi("hello", 80)
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("expected [hello], got %v", lines)
	}
}

func TestWrapText_Empty(t *testing.T) {
	lines := WrapTextWithAnsi("", 80)
	if len(lines) != 1 || lines[0] != "" {
		t.Errorf("expected [\"\"], got %v", lines)
	}
}

func TestWrapText_WordBreak(t *testing.T) {
	lines := WrapTextWithAnsi("hello world", 5)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "hello" {
		t.Errorf("first line: expected 'hello', got %q", lines[0])
	}
	if lines[1] != "world" {
		t.Errorf("second line: expected 'world', got %q", lines[1])
	}
}

func TestWrapText_LongWord(t *testing.T) {
	lines := WrapTextWithAnsi("abcdefghij", 5)
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "abcde" {
		t.Errorf("first line: expected 'abcde', got %q", lines[0])
	}
	if lines[1] != "fghij" {
		t.Errorf("second line: expected 'fghij', got %q", lines[1])
	}
}

func TestWrapText_PreservesAnsi(t *testing.T) {
	// Red text wrapping
	lines := WrapTextWithAnsi("\x1b[31mhello world\x1b[0m", 5)
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d", len(lines))
	}
	// Second line should have red color restored
	if !strings.Contains(lines[1], "\x1b[31m") {
		t.Errorf("expected ANSI red on second line, got %q", lines[1])
	}
}

func TestWrapText_Newlines(t *testing.T) {
	lines := WrapTextWithAnsi("line1\nline2\nline3", 80)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

// --- ApplyBackgroundToLine ---

func TestApplyBackgroundToLine(t *testing.T) {
	bg := func(s string) string { return "[" + s + "]" }
	result := ApplyBackgroundToLine("hi", 10, bg)
	// "hi" + 8 spaces, wrapped in brackets
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
	// Should include the ANSI code for red
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
