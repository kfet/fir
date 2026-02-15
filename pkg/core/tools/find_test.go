// Ported from: packages/coding-agent/src/core/tools/find.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/agent"
)

func execFind(t *testing.T, tool agent.AgentTool, params map[string]any) (agent.AgentToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), "test-call", params, nil)
}

func TestFindTool_BasicGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte(""), 0644)

	tool := NewFindTool(dir)
	result, err := execFind(t, tool, map[string]any{"pattern": "*.txt"})
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
	if strings.Contains(text, "c.go") {
		t.Error("should not contain c.go")
	}
}

func TestFindTool_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(""), 0644)

	tool := NewFindTool(dir)
	result, err := execFind(t, tool, map[string]any{"pattern": "*.xyz"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "No files found") {
		t.Errorf("expected 'No files found', got %q", result.Content[0].Text)
	}
}

func TestFindTool_SubdirectorySearch(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "deep.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte(""), 0644)

	tool := NewFindTool(dir)
	result, err := execFind(t, tool, map[string]any{"pattern": "*.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	// Both should be found
	if !strings.Contains(text, "deep.txt") {
		t.Error("expected deep.txt in output")
	}
	if !strings.Contains(text, "root.txt") {
		t.Error("expected root.txt in output")
	}
}

func TestFindTool_NonexistentPath(t *testing.T) {
	tool := NewFindTool("/tmp")
	_, err := execFind(t, tool, map[string]any{"pattern": "*.txt", "path": "/nonexistent/path"})
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestFindTool_EmptyPattern(t *testing.T) {
	tool := NewFindTool("/tmp")
	_, err := execFind(t, tool, map[string]any{"pattern": ""})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestFindTool_Cancellation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte(""), 0644)

	tool := NewFindTool(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Execute(ctx, "test", map[string]any{"pattern": "*.txt"}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestFindTool_WithSearchDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mydir")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "found.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "notfound.txt"), []byte(""), 0644)

	tool := NewFindTool(dir)
	result, err := execFind(t, tool, map[string]any{"pattern": "*.txt", "path": "mydir"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "found.txt") {
		t.Error("expected found.txt in output")
	}
}
