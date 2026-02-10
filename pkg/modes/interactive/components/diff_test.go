package components

import (
	"strings"
	"testing"
)

func TestParseDiffLine_Removed(t *testing.T) {
	prefix, lineNum, content, ok := parseDiffLine("-  5 old content")
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != "-" {
		t.Errorf("expected '-', got %q", prefix)
	}
	if strings.TrimSpace(lineNum) != "5" {
		t.Errorf("expected '  5', got %q", lineNum)
	}
	if content != "old content" {
		t.Errorf("expected 'old content', got %q", content)
	}
}

func TestParseDiffLine_Added(t *testing.T) {
	prefix, _, content, ok := parseDiffLine("+  6 new content")
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != "+" {
		t.Errorf("expected '+', got %q", prefix)
	}
	if content != "new content" {
		t.Errorf("expected 'new content', got %q", content)
	}
}

func TestParseDiffLine_Context(t *testing.T) {
	prefix, _, content, ok := parseDiffLine("   7 unchanged")
	if !ok {
		t.Fatal("expected ok")
	}
	if prefix != " " {
		t.Errorf("expected ' ', got %q", prefix)
	}
	if content != "unchanged" {
		t.Errorf("expected 'unchanged', got %q", content)
	}
}

func TestParseDiffLine_Invalid(t *testing.T) {
	_, _, _, ok := parseDiffLine("garbage")
	if ok {
		t.Error("expected not ok for invalid line")
	}
}

func TestReplaceTabs(t *testing.T) {
	result := replaceTabs("a\tb\tc")
	if result != "a   b   c" {
		t.Errorf("expected spaces, got %q", result)
	}
}

func TestSplitWords(t *testing.T) {
	words := splitWords("hello world  foo")
	if len(words) != 3 {
		t.Errorf("expected 3 words, got %d: %v", len(words), words)
	}
}

func TestSplitWords_Empty(t *testing.T) {
	words := splitWords("")
	if len(words) != 0 {
		t.Errorf("expected 0 words, got %d", len(words))
	}
}

func TestRenderDiff_BasicOutput(t *testing.T) {
	diff := "-  5 old line\n+  5 new line\n   6 context"
	result := RenderDiff(diff, nil)
	// Should contain ANSI escapes
	if !strings.Contains(result, "\x1b[") {
		t.Error("expected ANSI escapes in rendered diff")
	}
	// Should have 3 lines
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

func TestRenderDiff_Empty(t *testing.T) {
	result := RenderDiff("", nil)
	// Single empty line context
	if result == "" {
		t.Error("expected non-empty result for empty input")
	}
}
