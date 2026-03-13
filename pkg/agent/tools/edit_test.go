package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- edit-diff helpers ---

func TestDetectLineEnding(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lf only", "hello\nworld\n", "\n"},
		{"crlf only", "hello\r\nworld\r\n", "\r\n"},
		{"mixed crlf first", "hello\r\nworld\n", "\r\n"},
		{"no newlines", "hello", "\n"},
		{"empty", "", "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLineEnding(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeToLF(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello\r\nworld", "hello\nworld"},
		{"hello\rworld", "hello\nworld"},
		{"hello\r\nworld\rfoo\nbar", "hello\nworld\nfoo\nbar"},
		{"no newlines", "no newlines"},
	}
	for _, tt := range tests {
		got := normalizeToLF(tt.input)
		if got != tt.want {
			t.Errorf("normalizeToLF(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRestoreLineEndings(t *testing.T) {
	tests := []struct {
		input, ending, want string
	}{
		{"hello\nworld", "\n", "hello\nworld"},
		{"hello\nworld", "\r\n", "hello\r\nworld"},
	}
	for _, tt := range tests {
		got := restoreLineEndings(tt.input, tt.ending)
		if got != tt.want {
			t.Errorf("restoreLineEndings(%q, %q) = %q, want %q", tt.input, tt.ending, got, tt.want)
		}
	}
}

func TestNormalizeForFuzzyMatch(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips trailing whitespace", "hello  \nworld\t\n", "hello\nworld\n"},
		{"smart single quotes", "it\u2018s", "it's"},
		{"smart double quotes", "\u201Chello\u201D", "\"hello\""},
		{"en dash", "foo\u2013bar", "foo-bar"},
		{"em dash", "foo\u2014bar", "foo-bar"},
		{"nbsp", "hello\u00A0world", "hello world"},
		{"combined", "it\u2019s\u2014a\u00A0test  ", "it's-a test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeForFuzzyMatch(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFuzzyFindText_ExactMatch(t *testing.T) {
	result := fuzzyFindText("hello world", "world")
	if !result.found {
		t.Fatal("expected found")
	}
	if result.usedFuzzyMatch {
		t.Error("should not use fuzzy match for exact match")
	}
	if result.index != 6 {
		t.Errorf("index = %d, want 6", result.index)
	}
}

func TestFuzzyFindText_FuzzyMatch(t *testing.T) {
	// Smart quote should be normalized
	result := fuzzyFindText("it\u2019s a test", "it's a test")
	if !result.found {
		t.Fatal("expected found")
	}
	if !result.usedFuzzyMatch {
		t.Error("expected fuzzy match")
	}
}

func TestFuzzyFindText_TrailingWhitespace(t *testing.T) {
	result := fuzzyFindText("hello  \nworld", "hello\nworld")
	if !result.found {
		t.Fatal("expected found via trailing whitespace normalization")
	}
	if !result.usedFuzzyMatch {
		t.Error("expected fuzzy match")
	}
}

func TestFuzzyFindText_NotFound(t *testing.T) {
	result := fuzzyFindText("hello world", "xyz")
	if result.found {
		t.Error("expected not found")
	}
}

// --- EditTool ---

func TestEditTool_BasicReplacement(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("hello world"), 0644)

	tool := NewEditTool(dir)
	result, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "hello",
		"newText": "goodbye",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "Successfully") {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}

	content, _ := os.ReadFile(filePath)
	if string(content) != "goodbye world" {
		t.Errorf("file content = %q, want 'goodbye world'", string(content))
	}
}

func TestEditTool_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "nonexistent.txt",
		"oldText": "x",
		"newText": "y",
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEditTool_TextNotFound(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "xyz",
		"newText": "abc",
	}, nil)
	if err == nil {
		t.Fatal("expected error for text not found")
	}
	if !strings.Contains(err.Error(), "Could not find") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEditTool_MultipleOccurrences(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("foo bar foo"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "foo",
		"newText": "baz",
	}, nil)
	if err == nil {
		t.Fatal("expected error for multiple occurrences")
	}
	if !strings.Contains(err.Error(), "2 occurrences") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEditTool_NoChange(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "hello",
		"newText": "hello",
	}, nil)
	if err == nil {
		t.Fatal("expected error for no change")
	}
	if !strings.Contains(err.Error(), "No changes") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEditTool_BOM(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("\uFEFFhello world"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "hello",
		"newText": "goodbye",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	if !strings.HasPrefix(string(content), "\uFEFF") {
		t.Error("BOM should be preserved")
	}
	if !strings.Contains(string(content), "goodbye world") {
		t.Errorf("file content = %q", string(content))
	}
}

func TestEditTool_CRLFPreservation(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("hello\r\nworld\r\n"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "hello",
		"newText": "goodbye",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	if string(content) != "goodbye\r\nworld\r\n" {
		t.Errorf("file content = %q, want 'goodbye\\r\\nworld\\r\\n'", string(content))
	}
}

func TestEditTool_FuzzyMatchSmartQuotes(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	// File has smart quotes
	os.WriteFile(filePath, []byte("it\u2019s a test"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "it's a test",
		"newText": "it was a test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	if string(content) != "it was a test" {
		t.Errorf("file content = %q", string(content))
	}
}

func TestEditTool_MultilineReplacement(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("line1\nline2\nline3\nline4"), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "line2\nline3",
		"newText": "new2\nnew3\nnew3b",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	if string(content) != "line1\nnew2\nnew3\nnew3b\nline4" {
		t.Errorf("file content = %q", string(content))
	}
}

func TestEditTool_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("hello world"), 0644)

	tool := NewEditTool(dir)
	result, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    filePath, // absolute
		"oldText": "hello",
		"newText": "goodbye",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "Successfully") {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}
}

func TestEditTool_Abort(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := NewEditTool(dir)
	_, err := tool.Execute(ctx, "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "hello",
		"newText": "goodbye",
	}, nil)
	if err == nil {
		t.Fatal("expected abort error")
	}
}

func TestEditTool_FuzzyMatchPreservesUnrelatedContent(t *testing.T) {
	// Regression test: fuzzy matching should only affect the matched region,
	// not normalize the entire file (e.g., smart quotes elsewhere should survive).
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	// File has smart quotes in TWO places, but we only edit the second line.
	// The smart quotes on the first line must survive untouched.
	original := "line1: \u201Chello\u201D\nline2: it\u2019s here\nline3: keep\u2014this"
	os.WriteFile(filePath, []byte(original), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "it's here", // ASCII apostrophe matches smart quote via fuzzy
		"newText": "it was replaced",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	got := string(content)
	// Line 1's smart quotes must be preserved
	if !strings.Contains(got, "\u201Chello\u201D") {
		t.Errorf("smart quotes on line 1 were normalized; got %q", got)
	}
	// Line 3's em dash must be preserved
	if !strings.Contains(got, "keep\u2014this") {
		t.Errorf("em dash on line 3 was normalized; got %q", got)
	}
	// The replacement should have been applied
	if !strings.Contains(got, "it was replaced") {
		t.Errorf("replacement not applied; got %q", got)
	}
}

func TestFuzzyFindText_FuzzyMatchReturnsOriginalContent(t *testing.T) {
	// Verify that fuzzy match returns the original content for replacement,
	// not the normalized version.
	content := "prefix \u201Chello\u201D suffix"
	result := fuzzyFindText(content, "\"hello\"") // ASCII quotes match smart quotes
	if !result.found {
		t.Fatal("expected found")
	}
	if !result.usedFuzzyMatch {
		t.Error("expected fuzzy match")
	}
	if result.contentForReplacement != content {
		t.Errorf("contentForReplacement should be original content, got %q", result.contentForReplacement)
	}
	// The match region in the original should cover the smart-quoted hello
	matched := content[result.index : result.index+result.matchLength]
	if !strings.Contains(matched, "hello") {
		t.Errorf("matched region = %q, should contain 'hello'", matched)
	}
}

func TestMapFuzzyToOriginal_MultiByte(t *testing.T) {
	// \u2014 (em dash, 3 bytes) maps to "-" (1 byte)
	original := "before\u2014after"
	normalized := normalizeForFuzzyMatch(original)
	// normalized = "before-after"

	// Find "-after" in normalized
	target := "-after"
	idx := strings.Index(normalized, target)
	if idx == -1 {
		t.Fatal("expected to find target in normalized")
	}
	origStart, origEnd := mapFuzzyToOriginal(original, normalized, idx, idx+len(target))
	matched := original[origStart:origEnd]
	if matched != "\u2014after" {
		t.Errorf("expected '\u2014after', got %q", matched)
	}
}

func TestMapFuzzyToOriginal_TrailingWhitespaceAtBoundary(t *testing.T) {
	// Trailing whitespace is stripped per line; match ends exactly at line boundary
	original := "hello  \nworld"
	normalized := normalizeForFuzzyMatch(original) // "hello\nworld"

	target := "hello\nworld"
	idx := strings.Index(normalized, target)
	if idx == -1 {
		t.Fatal("expected to find target")
	}
	origStart, origEnd := mapFuzzyToOriginal(original, normalized, idx, idx+len(target))
	matched := original[origStart:origEnd]
	// The match in the original should cover "hello  \nworld" (the original)
	if !strings.Contains(matched, "hello") || !strings.Contains(matched, "world") {
		t.Errorf("unexpected matched region: %q", matched)
	}
}

func TestMapFuzzyToOriginal_MixedNormalizations(t *testing.T) {
	// Smart quotes + em dash + nbsp all in the same match region
	original := "\u201Chello\u201D\u2014world\u00A0here"
	normalized := normalizeForFuzzyMatch(original) // "\"hello\"-world here"

	target := "\"hello\"-world here"
	idx := strings.Index(normalized, target)
	if idx == -1 {
		t.Fatal("expected to find target")
	}
	origStart, origEnd := mapFuzzyToOriginal(original, normalized, idx, idx+len(target))
	matched := original[origStart:origEnd]
	if matched != original {
		t.Errorf("expected entire original, got %q", matched)
	}
}

func TestMapFuzzyToOriginal_MatchAtStart(t *testing.T) {
	original := "\u2019s a test suffix"
	normalized := normalizeForFuzzyMatch(original) // "'s a test suffix"

	target := "'s a test"
	idx := strings.Index(normalized, target)
	if idx == -1 {
		t.Fatal("expected to find target")
	}
	origStart, origEnd := mapFuzzyToOriginal(original, normalized, idx, idx+len(target))
	if origStart != 0 {
		t.Errorf("expected origStart=0, got %d", origStart)
	}
	matched := original[origStart:origEnd]
	if !strings.HasPrefix(matched, "\u2019") {
		t.Errorf("expected to start with smart quote, got %q", matched)
	}
}

func TestMapFuzzyToOriginal_MatchAtEnd(t *testing.T) {
	original := "prefix here\u2019s"
	normalized := normalizeForFuzzyMatch(original) // "prefix here's"

	target := "here's"
	idx := strings.Index(normalized, target)
	if idx == -1 {
		t.Fatal("expected to find target")
	}
	origStart, origEnd := mapFuzzyToOriginal(original, normalized, idx, idx+len(target))
	matched := original[origStart:origEnd]
	if !strings.HasSuffix(matched, "\u2019s") {
		t.Errorf("expected to end with smart quote, got %q", matched)
	}
	if origEnd != len(original) {
		t.Errorf("expected origEnd=%d, got %d", len(original), origEnd)
	}
}

func TestEditTool_FuzzyMatchMultiByte(t *testing.T) {
	// Full integration test: fuzzy match with multi-byte Unicode doesn't corrupt the file
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	original := "line1: \u201Chello\u201D\nline2: keep\u2014this\nline3: end"
	os.WriteFile(filePath, []byte(original), 0644)

	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"path":    "test.txt",
		"oldText": "keep-this", // ASCII dash matches em dash via fuzzy
		"newText": "keep-replaced",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filePath)
	got := string(content)
	// Line 1 smart quotes must be preserved
	if !strings.Contains(got, "\u201Chello\u201D") {
		t.Errorf("smart quotes on line 1 corrupted: %q", got)
	}
	// Replacement should be applied
	if !strings.Contains(got, "keep-replaced") {
		t.Errorf("replacement not applied: %q", got)
	}
	// Line 3 should be intact
	if !strings.Contains(got, "line3: end") {
		t.Errorf("line 3 corrupted: %q", got)
	}
}

func TestEditTool_PathRequired(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditTool(dir)
	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"oldText": "x",
		"newText": "y",
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestEditToolWithReadWriter_SuccessfulRoundTrip(t *testing.T) {
	dir := t.TempDir()
	initial := "hello world"
	current := initial

	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return current, nil
	})
	writeFn := WriteFileFn(func(_ context.Context, _ string, content string) error {
		current = content
		return nil
	})

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path":    filepath.Join(dir, "file.txt"),
		"oldText": "world",
		"newText": "Go",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "Successfully replaced") {
		t.Errorf("unexpected result: %q", result.Content[0].Text)
	}
	if current != "hello Go" {
		t.Errorf("file content = %q, want %q", current, "hello Go")
	}
}

func TestEditToolWithReadWriter_TextNotFound(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "hello world", nil
	})
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path":    filepath.Join(dir, "file.txt"),
		"oldText": "not present",
		"newText": "x",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Could not find") {
		t.Errorf("expected text-not-found error, got %v", err)
	}
}

func TestEditToolWithReadWriter_MultipleOccurrences(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "ab ab ab", nil
	})
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path":    filepath.Join(dir, "file.txt"),
		"oldText": "ab",
		"newText": "x",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "occurrences") {
		t.Errorf("expected multiple-occurrences error, got %v", err)
	}
}

func TestEditToolWithReadWriter_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) { return "", nil })
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": "", "oldText": "x", "newText": "y",
	}, nil)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestEditToolWithReadWriter_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) { return "ab", nil })
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Execute(ctx, "c1", map[string]any{
		"path": filepath.Join(dir, "f.txt"), "oldText": "ab", "newText": "cd",
	}, nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestEditToolWithReadWriter_ReadFnError(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("read failed")
	})
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })

	tool := NewEditToolWithReadWriter(dir, readFn, writeFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": filepath.Join(dir, "f.txt"), "oldText": "x", "newText": "y",
	}, nil)
	if err == nil {
		t.Error("expected error when readFn fails")
	}
}
