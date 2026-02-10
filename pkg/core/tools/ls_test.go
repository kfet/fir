// Ported from: packages/coding-agent/src/core/tools/ls.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/agent"
)

func execLs(t *testing.T, tool agent.AgentTool, params map[string]any) (agent.AgentToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), "test-call", params, nil)
}

func TestLsTool_BasicDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte(""), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].Text
	if !strings.Contains(text, "a.txt") {
		t.Error("expected a.txt in output")
	}
	if !strings.Contains(text, "b.txt") {
		t.Error("expected b.txt in output")
	}
	if !strings.Contains(text, "subdir/") {
		t.Error("expected subdir/ in output")
	}
}

func TestLsTool_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content[0].Text != "(empty directory)" {
		t.Errorf("expected '(empty directory)', got %q", result.Content[0].Text)
	}
}

func TestLsTool_NonexistentPath(t *testing.T) {
	tool := NewLsTool("/tmp")
	_, err := execLs(t, tool, map[string]any{"path": "/nonexistent/path"})
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestLsTool_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	os.WriteFile(file, []byte(""), 0644)

	tool := NewLsTool(dir)
	_, err := execLs(t, tool, map[string]any{"path": "file.txt"})
	if err == nil {
		t.Fatal("expected error for file path")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' error, got: %v", err)
	}
}

func TestLsTool_WithLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%02d.txt", i)), []byte(""), 0644)
	}

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{"limit": float64(3)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "entries limit reached") {
		t.Error("expected entry limit notice")
	}
}

func TestLsTool_DotFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte(""), 0644)

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, ".hidden") {
		t.Error("expected .hidden in output")
	}
}

func TestLsTool_SortedCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Banana"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "apple"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "Cherry"), []byte(""), 0644)

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	// apple, Banana, Cherry (case-insensitive sort)
	if lines[0] != "apple" || lines[1] != "Banana" || lines[2] != "Cherry" {
		t.Errorf("unexpected order: %v", lines)
	}
}

func TestLsTool_Cancellation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0644)

	tool := NewLsTool(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Execute(ctx, "test", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestLsTool_SubdirectoryPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "file.txt"), []byte(""), 0644)

	tool := NewLsTool(dir)
	result, err := execLs(t, tool, map[string]any{"path": "sub"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "file.txt") {
		t.Error("expected file.txt in subdirectory listing")
	}
}
