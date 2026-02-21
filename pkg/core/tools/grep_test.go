// Ported from: packages/coding-agent/src/core/tools/grep.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func execGrep(t *testing.T, tool agent.AgentTool, params map[string]any) (agent.AgentToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), "test-call", params, nil)
}

func TestGrepTool_BasicSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\nfoo bar\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("baz qux\nhello again\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "hello") {
		t.Error("expected 'hello' in output")
	}
	// Should find matches in both files
	if !strings.Contains(text, "a.txt") {
		t.Error("expected a.txt in output")
	}
	if !strings.Contains(text, "b.txt") {
		t.Error("expected b.txt in output")
	}
}

func TestGrepTool_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "No matches found") {
		t.Errorf("expected 'No matches found', got %q", result.Content[0].Text)
	}
}

func TestGrepTool_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("Hello World\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "hello", "ignoreCase": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Content[0].Text, "No matches") {
		t.Error("expected a match with case-insensitive search")
	}
}

func TestGrepTool_LiteralSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo.bar\nfoo*bar\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "foo.bar", "literal": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "No matches") {
		t.Error("expected a match for literal search")
	}
}

func TestGrepTool_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("hello\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "hello", "glob": "*.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "b.go") {
		t.Error("should not match .go files with *.txt glob")
	}
}

func TestGrepTool_NonexistentPath(t *testing.T) {
	tool := NewGrepTool("/tmp")
	_, err := execGrep(t, tool, map[string]any{"pattern": "test", "path": "/nonexistent/path"})
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestGrepTool_EmptyPattern(t *testing.T) {
	tool := NewGrepTool("/tmp")
	_, err := execGrep(t, tool, map[string]any{"pattern": ""})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestGrepTool_Cancellation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content\n"), 0644)

	tool := NewGrepTool(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Execute(ctx, "test", map[string]any{"pattern": "content"}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestGrepTool_LineNumbers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("line1\nline2\ntarget line\nline4\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "target"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	// Should include line number 3
	if !strings.Contains(text, ":3:") {
		t.Errorf("expected line number 3 in output, got: %q", text)
	}
}

// --- grepFallback tests ---
// These test the grep(1) fallback used when ripgrep is not available.

func TestGrepFallback_BasicSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\nfoo bar\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("baz qux\nhello again\n"), 0644)

	result, err := grepFallback(context.Background(), "hello", dir, true, false, false, "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "hello") {
		t.Error("expected 'hello' in output")
	}
}

func TestGrepFallback_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0644)

	result, err := grepFallback(context.Background(), "nonexistent", dir, true, false, false, "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "No matches found") {
		t.Errorf("expected 'No matches found', got %q", result.Content[0].Text)
	}
}

func TestGrepFallback_CaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("Hello World\n"), 0644)

	result, err := grepFallback(context.Background(), "hello", dir, true, true, false, "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Content[0].Text, "No matches") {
		t.Error("expected a match with case-insensitive search")
	}
}

func TestGrepFallback_Literal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo.bar\nfooXbar\n"), 0644)

	result, err := grepFallback(context.Background(), "foo.bar", dir, true, false, true, "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	// Literal mode: "foo.bar" matches only "foo.bar", not "fooXbar"
	if strings.Contains(text, "fooXbar") {
		t.Error("literal mode should not match fooXbar")
	}
}

func TestGrepFallback_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("hello\n"), 0644)

	result, err := grepFallback(context.Background(), "hello", dir, true, false, false, "*.txt", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "b.go") {
		t.Error("should not match .go files with *.txt glob")
	}
}

func TestGrepFallback_Limit(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 0; i < 100; i++ {
		content.WriteString("match line\n")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content.String()), 0644)

	result, err := grepFallback(context.Background(), "match", dir, true, false, false, "", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	// Should be limited (5 result lines + possible notice)
	if len(lines) > 10 {
		t.Errorf("expected limited output, got %d lines", len(lines))
	}
	if !strings.Contains(text, "limit reached") {
		t.Error("expected limit reached notice")
	}
}

func TestGrepFallback_SingleFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("hello world\nfoo bar\nhello again\n"), 0644)

	result, err := grepFallback(context.Background(), "hello", filePath, false, false, false, "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "No matches") {
		t.Error("expected matches in single file")
	}
}

func TestGrepTool_SingleFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world\nfoo bar\nhello again\n"), 0644)

	tool := NewGrepTool(dir)
	result, err := execGrep(t, tool, map[string]any{"pattern": "hello", "path": "test.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "No matches") {
		t.Error("expected matches in single file")
	}
}
