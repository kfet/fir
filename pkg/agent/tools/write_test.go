package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTool_NewFile(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    "hello.txt",
		"content": "hello world",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text == "" {
		t.Error("expected non-empty result text")
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want %q", data, "hello world")
	}
}

func TestWriteTool_OverwriteFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("old content"), 0644)

	tool := NewWriteTool(dir)
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    "existing.txt",
		"content": "new content",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "existing.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "new content" {
		t.Errorf("file content = %q, want %q", data, "new content")
	}
}

func TestWriteTool_CreateDirectories(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    "sub/dir/file.txt",
		"content": "nested content",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sub", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("file content = %q", data)
	}
}

func TestWriteTool_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	absPath := filepath.Join(dir, "abs.txt")

	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    absPath,
		"content": "absolute",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "absolute" {
		t.Errorf("file content = %q", data)
	}
}

func TestWriteTool_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    "empty.txt",
		"content": "",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "" {
		t.Errorf("file content = %q, want empty", data)
	}
}

func TestWriteTool_MissingPath(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"content": "no path",
	}, nil)
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestWriteTool_Cancelled(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := tool.Execute(ctx, "call-1", map[string]any{
		"path":    "cancelled.txt",
		"content": "should not write",
	}, nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestWriteTool_ByteCount(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	content := "hello world"
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":    "count.txt",
		"content": content,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "Successfully wrote 11 bytes to count.txt"
	if result.Content[0].Text != expected {
		t.Errorf("result text = %q, want %q", result.Content[0].Text, expected)
	}
}

func TestWriteToolWithWriter_DelegatesAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	var gotPath, gotContent string
	writeFn := WriteFileFn(func(_ context.Context, path, content string) error {
		gotPath = path
		gotContent = content
		return nil
	})
	tool := NewWriteToolWithWriter(dir, writeFn)
	absFile := filepath.Join(dir, "out.txt")
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path":    absFile,
		"content": "hello world",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != absFile {
		t.Errorf("writeFn got path %q, want %q", gotPath, absFile)
	}
	if gotContent != "hello world" {
		t.Errorf("writeFn got content %q, want %q", gotContent, "hello world")
	}
	if !strings.Contains(result.Content[0].Text, "bytes to") {
		t.Errorf("unexpected result text: %q", result.Content[0].Text)
	}
}

func TestWriteToolWithWriter_EmptyPathReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })
	tool := NewWriteToolWithWriter(dir, writeFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": "", "content": "data",
	}, nil)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestWriteToolWithWriter_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })
	tool := NewWriteToolWithWriter(dir, writeFn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := tool.Execute(ctx, "c1", map[string]any{
		"path": filepath.Join(dir, "f.txt"), "content": "x",
	}, nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestWriteToolWithWriter_BytesWrittenMessage(t *testing.T) {
	dir := t.TempDir()
	writeFn := WriteFileFn(func(_ context.Context, _, _ string) error { return nil })
	tool := NewWriteToolWithWriter(dir, writeFn)
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": filepath.Join(dir, "f.txt"), "content": "hello",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "5 bytes") {
		t.Errorf("expected '5 bytes' in message, got %q", result.Content[0].Text)
	}
}
