package components

import (
	"strings"
	"testing"
)

func TestTruncateToVisualLines_Empty(t *testing.T) {
	result := TruncateToVisualLines("", 10, 80, 0)
	if result.VisualLines != nil {
		t.Errorf("expected nil, got %v", result.VisualLines)
	}
	if result.SkippedCount != 0 {
		t.Errorf("expected 0 skipped, got %d", result.SkippedCount)
	}
}

func TestTruncateToVisualLines_FitsWithin(t *testing.T) {
	text := "line1\nline2\nline3"
	result := TruncateToVisualLines(text, 10, 80, 0)
	if len(result.VisualLines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(result.VisualLines))
	}
	if result.SkippedCount != 0 {
		t.Errorf("expected 0 skipped, got %d", result.SkippedCount)
	}
}

func TestTruncateToVisualLines_Truncates(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	text := strings.Join(lines, "\n")
	result := TruncateToVisualLines(text, 5, 80, 0)
	if len(result.VisualLines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(result.VisualLines))
	}
	if result.SkippedCount != 15 {
		t.Errorf("expected 15 skipped, got %d", result.SkippedCount)
	}
}

func TestTruncateToVisualLines_WrappedLines(t *testing.T) {
	// A long line that wraps at width 10
	longLine := "abcdefghijklmnopqrstuvwxyz" // 26 chars, wraps to ~3 lines at width 10
	result := TruncateToVisualLines(longLine, 2, 10, 0)
	if len(result.VisualLines) != 2 {
		t.Errorf("expected 2 visual lines, got %d", len(result.VisualLines))
	}
	if result.SkippedCount <= 0 {
		t.Errorf("expected some skipped lines, got %d", result.SkippedCount)
	}
}

func TestTruncateToVisualLines_WithPadding(t *testing.T) {
	text := "short"
	result := TruncateToVisualLines(text, 10, 80, 1)
	if len(result.VisualLines) != 1 {
		t.Errorf("expected 1 line, got %d", len(result.VisualLines))
	}
}
