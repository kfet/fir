package tools

import (
	"strings"
	"testing"
)

func TestGenerateDiffString_BasicInsert(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nline2\nnewline\nline3"

	result := GenerateDiffString(old, new, 4)
	if result.Diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(result.Diff, "+") {
		t.Error("expected + for added line")
	}
	if !strings.Contains(result.Diff, "newline") {
		t.Error("expected 'newline' in diff")
	}
	if result.FirstChangedLine == nil {
		t.Fatal("expected firstChangedLine")
	}
}

func TestGenerateDiffString_BasicRemove(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nline3"

	result := GenerateDiffString(old, new, 4)
	if result.Diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(result.Diff, "-") {
		t.Error("expected - for removed line")
	}
}

func TestGenerateDiffString_Replace(t *testing.T) {
	old := "line1\nold\nline3"
	new := "line1\nnew\nline3"

	result := GenerateDiffString(old, new, 4)
	if !strings.Contains(result.Diff, "old") {
		t.Error("expected 'old' in diff as removed")
	}
	if !strings.Contains(result.Diff, "new") {
		t.Error("expected 'new' in diff as added")
	}
}

func TestGenerateDiffString_NoChange(t *testing.T) {
	content := "line1\nline2\nline3"
	result := GenerateDiffString(content, content, 4)
	if result.Diff != "" {
		t.Errorf("expected empty diff for identical content, got: %q", result.Diff)
	}
	if result.FirstChangedLine != nil {
		t.Error("expected nil firstChangedLine for no changes")
	}
}

func TestGenerateDiffString_ContextLines(t *testing.T) {
	// Build a file with many lines, change one in the middle
	var oldLines, newLines []string
	for i := 0; i < 20; i++ {
		oldLines = append(oldLines, "line"+string(rune('A'+i)))
		newLines = append(newLines, "line"+string(rune('A'+i)))
	}
	newLines[10] = "CHANGED"

	old := strings.Join(oldLines, "\n")
	new := strings.Join(newLines, "\n")

	result := GenerateDiffString(old, new, 2)
	if result.Diff == "" {
		t.Fatal("expected non-empty diff")
	}
	// Should contain ellipsis for skipped context
	if !strings.Contains(result.Diff, "...") {
		t.Error("expected '...' for skipped context")
	}
}

func TestGenerateDiffString_FirstChangedLine(t *testing.T) {
	old := "line1\nline2\nline3\nline4"
	new := "line1\nline2\nCHANGED\nline4"

	result := GenerateDiffString(old, new, 4)
	if result.FirstChangedLine == nil {
		t.Fatal("expected firstChangedLine")
	}
	if *result.FirstChangedLine != 3 {
		t.Errorf("firstChangedLine = %d, want 3", *result.FirstChangedLine)
	}
}

func TestComputeDiffParts_EmptyToContent(t *testing.T) {
	parts := computeDiffParts([]string{}, []string{"a", "b"})
	if len(parts) == 0 {
		t.Fatal("expected parts")
	}
	addedCount := 0
	for _, p := range parts {
		if p.added {
			addedCount += len(p.lines)
		}
	}
	if addedCount != 2 {
		t.Errorf("expected 2 added lines, got %d", addedCount)
	}
}

func TestComputeDiffParts_ContentToEmpty(t *testing.T) {
	parts := computeDiffParts([]string{"a", "b"}, []string{})
	removedCount := 0
	for _, p := range parts {
		if p.removed {
			removedCount += len(p.lines)
		}
	}
	if removedCount != 2 {
		t.Errorf("expected 2 removed lines, got %d", removedCount)
	}
}

func TestComputeDiffParts_Identical(t *testing.T) {
	lines := []string{"a", "b", "c"}
	parts := computeDiffParts(lines, lines)
	for _, p := range parts {
		if p.added || p.removed {
			t.Error("expected no changes for identical content")
		}
	}
}

func TestSimpleDiffParts_SharedPrefixAndSuffix(t *testing.T) {
	old := []string{"a", "b", "c", "d", "e"}
	new := []string{"a", "b", "X", "d", "e"}

	parts := simpleDiffParts(old, new)
	hasRemoved := false
	hasAdded := false
	for _, p := range parts {
		if p.removed {
			hasRemoved = true
		}
		if p.added {
			hasAdded = true
		}
	}
	if !hasRemoved || !hasAdded {
		t.Error("expected both removed and added parts")
	}
}
