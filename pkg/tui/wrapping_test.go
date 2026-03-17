package tui

import (
	"strings"
	"testing"
)

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
	lines := WrapTextWithAnsi("\x1b[31mhello world\x1b[0m", 5)
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d", len(lines))
	}
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
