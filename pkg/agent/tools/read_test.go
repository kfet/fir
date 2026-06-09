// Ported from: packages/coding-agent/src/core/tools/read.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func execRead(t *testing.T, tool agent.AgentTool, params map[string]any) (agent.AgentToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), "test-call", params, nil)
}

func TestReadTool_BasicFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	os.WriteFile(file, []byte("line1\nline2\nline3"), 0644)

	tool := NewReadTool(dir)
	result, err := execRead(t, tool, map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks (body + hash), got %d", len(result.Content))
	}
	if result.Content[0].Text != "line1\nline2\nline3" {
		t.Errorf("unexpected content: %q", result.Content[0].Text)
	}
	if !strings.HasPrefix(result.Content[1].Text, "[hash: ") {
		t.Errorf("expected hash block, got %q", result.Content[1].Text)
	}
}

func TestReadTool_WithOffset(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "lines.txt")
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = strings.Repeat("x", 10)
	}
	os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0644)

	tool := NewReadTool(dir)
	result, err := execRead(t, tool, map[string]any{"path": "lines.txt", "offset": float64(5)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	// Should start from line 5 (0-indexed: line 4)
	resultLines := strings.Split(text, "\n")
	// Lines 5-10 = 6 lines of content
	if len(resultLines) < 6 {
		t.Errorf("expected at least 6 lines, got %d", len(resultLines))
	}
}

func TestReadTool_WithLimit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "lines.txt")
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "line" + strings.Repeat("x", 5)
	}
	os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0644)

	tool := NewReadTool(dir)
	result, err := execRead(t, tool, map[string]any{"path": "lines.txt", "limit": float64(3)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	// Should contain hint about more lines
	if !strings.Contains(text, "more lines") {
		t.Errorf("expected 'more lines' notice, got: %q", text)
	}
}

func TestReadTool_OffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "short.txt")
	os.WriteFile(file, []byte("one\ntwo"), 0644)

	tool := NewReadTool(dir)
	_, err := execRead(t, tool, map[string]any{"path": "short.txt", "offset": float64(100)})
	if err == nil {
		t.Fatal("expected error for out-of-bounds offset")
	}
	if !strings.Contains(err.Error(), "beyond end of file") {
		t.Errorf("expected 'beyond end of file' error, got: %v", err)
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	tool := NewReadTool(dir)
	_, err := execRead(t, tool, map[string]any{"path": "nonexistent.txt"})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestReadTool_EmptyPath(t *testing.T) {
	tool := NewReadTool("/tmp")
	_, err := execRead(t, tool, map[string]any{"path": ""})
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReadTool_Truncation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "big.txt")
	// Create a file with more than DefaultMaxLines lines
	lines := make([]string, DefaultMaxLines+100)
	for i := range lines {
		lines[i] = "content"
	}
	os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0644)

	tool := NewReadTool(dir)
	result, err := execRead(t, tool, map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "Showing lines") {
		t.Errorf("expected truncation notice, got: %s", text[:min(200, len(text))])
	}
}

func TestReadTool_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "abs.txt")
	os.WriteFile(file, []byte("absolute content"), 0644)

	tool := NewReadTool("/some/other/dir")
	result, err := execRead(t, tool, map[string]any{"path": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content[0].Text != "absolute content" {
		t.Errorf("unexpected content: %q", result.Content[0].Text)
	}
}

func TestReadTool_Cancellation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cancel.txt")
	os.WriteFile(file, []byte("content"), 0644)

	tool := NewReadTool(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := tool.Execute(ctx, "test", map[string]any{"path": "cancel.txt"}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestReadTool_Directory(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)

	tool := NewReadTool(dir)
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path": "subdir",
	}, nil)
	if err == nil {
		t.Error("expected error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error = %q, want contains 'directory'", err.Error())
	}
}

func TestReadTool_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ol.txt"), []byte("line1\nline2\nline3\nline4\nline5"), 0644)

	tool := NewReadTool(dir)
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"path":   "ol.txt",
		"offset": float64(2),
		"limit":  float64(2),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "line1") {
		t.Error("should not contain line1")
	}
	if !strings.Contains(text, "line2") || !strings.Contains(text, "line3") {
		t.Errorf("should contain line2 and line3: %q", text)
	}
	if !strings.Contains(text, "more lines") {
		t.Error("should have 'more lines' notice")
	}
}

func TestReadTool_IfHashIgnoredForPartialRead(t *testing.T) {
	// Regression: if_hash is a whole-file identity, so it must NOT short-circuit
	// a partial (offset/limit) read — otherwise the model gets "unchanged" for a
	// slice it has never seen. Partial reads carry no hash and ignore if_hash.
	dir := t.TempDir()
	file := filepath.Join(dir, "h.txt")
	os.WriteFile(file, []byte("l1\nl2\nl3\nl4\nl5"), 0644)

	tool := NewReadTool(dir)
	// Learn the whole-file hash from a full read.
	full, err := execRead(t, tool, map[string]any{"path": "h.txt"})
	if err != nil {
		t.Fatal(err)
	}
	hash := full.Details.(*ReadToolDetails).Hash

	// A partial read carrying that hash must return the actual slice, not a stub.
	res, err := execRead(t, tool, map[string]any{"path": "h.txt", "offset": float64(3), "limit": float64(2), "if_hash": hash})
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := res.Details.(*ReadToolDetails); ok && d.Unchanged {
		t.Fatal("partial read must not return an unchanged stub for a whole-file if_hash")
	}
	if !strings.Contains(res.Content[0].Text, "l3") {
		t.Errorf("expected the requested slice (l3..), got %q", res.Content[0].Text)
	}
}

func TestReadTool_IfHashMatchReturnsStub(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "h.txt")
	os.WriteFile(file, []byte("alpha\nbeta\ngamma"), 0644)

	tool := NewReadTool(dir)
	// First read to learn the hash.
	res1, err := execRead(t, tool, map[string]any{"path": "h.txt"})
	if err != nil {
		t.Fatal(err)
	}
	d1, ok := res1.Details.(*ReadToolDetails)
	if !ok || d1.Hash == "" {
		t.Fatalf("expected hash detail, got %+v", res1.Details)
	}
	hash := d1.Hash

	// Second read with matching if_hash -> stub, no body.
	res2, err := execRead(t, tool, map[string]any{"path": "h.txt", "if_hash": hash})
	if err != nil {
		t.Fatal(err)
	}
	d2, ok := res2.Details.(*ReadToolDetails)
	if !ok || !d2.Unchanged || d2.Hash != hash {
		t.Fatalf("expected unchanged stub, got %+v", res2.Details)
	}
	if strings.Contains(res2.Content[0].Text, "alpha") {
		t.Errorf("stub should not contain body: %q", res2.Content[0].Text)
	}
}

func TestReadTool_IfHashMismatchReturnsFull(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "h.txt")
	os.WriteFile(file, []byte("original"), 0644)

	tool := NewReadTool(dir)
	// Stale/wrong hash -> full read with fresh hash.
	res, err := execRead(t, tool, map[string]any{"path": "h.txt", "if_hash": "deadbeefdeadbeef"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "original") {
		t.Errorf("expected full body, got %q", res.Content[0].Text)
	}
	d, ok := res.Details.(*ReadToolDetails)
	if !ok || d.Hash == "" || d.Unchanged {
		t.Fatalf("expected full read with hash, got %+v", res.Details)
	}
}

func TestReadTool_IfHashInvalidatedByChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "h.txt")
	os.WriteFile(file, []byte("v1"), 0644)

	tool := NewReadTool(dir)
	res1, _ := execRead(t, tool, map[string]any{"path": "h.txt"})
	hash := res1.Details.(*ReadToolDetails).Hash

	// Outside change: rewrite the file. The recomputed hash differs, so the
	// same if_hash now yields the fresh content.
	os.WriteFile(file, []byte("v2-changed"), 0644)
	res2, err := execRead(t, tool, map[string]any{"path": "h.txt", "if_hash": hash})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Content[0].Text, "v2-changed") {
		t.Errorf("expected fresh content after change, got %q", res2.Content[0].Text)
	}
}

func TestReadToolWithReader_IfHashMatch(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "delegated body", nil
	})
	tool := NewReadToolWithReader(dir, readFn)
	res1, err := tool.Execute(context.Background(), "c1", map[string]any{"path": filepath.Join(dir, "f.txt")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := res1.Details.(*ReadToolDetails).Hash
	if hash == "" {
		t.Fatal("expected hash from delegated read")
	}
	res2, err := tool.Execute(context.Background(), "c2", map[string]any{"path": filepath.Join(dir, "f.txt"), "if_hash": hash}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res2.Details.(*ReadToolDetails)
	if !ok || !d.Unchanged {
		t.Fatalf("expected unchanged stub, got %+v", res2.Details)
	}
}

func TestReadToolWithReader_DelegatesTextRead(t *testing.T) {
	dir := t.TempDir()
	called := false
	readFn := ReadFileFn(func(_ context.Context, path string) (string, error) {
		called = true
		return "delegated content", nil
	})
	tool := NewReadToolWithReader(dir, readFn)
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": filepath.Join(dir, "file.txt"),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("readFn was not called")
	}
	if !strings.Contains(result.Content[0].Text, "delegated content") {
		t.Errorf("unexpected content: %q", result.Content[0].Text)
	}
}

func TestReadToolWithReader_ImageFallsBackToLocal(t *testing.T) {
	dir := t.TempDir()
	// Write a tiny valid 1x1 PNG
	pngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC,
		0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, // IEND chunk
		0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	imgPath := filepath.Join(dir, "img.png")
	if err := os.WriteFile(imgPath, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	delegateCalled := false
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		delegateCalled = true
		return "", nil
	})
	tool := NewReadToolWithReader(dir, readFn)
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": imgPath,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error reading image: %v", err)
	}
	if delegateCalled {
		t.Error("readFn should not be called for image files")
	}
	// Result should have image content type somewhere in the content
	hasImage := false
	for _, c := range result.Content {
		if c.Type == "image" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		t.Errorf("expected image content, got %+v", result.Content)
	}
}

func TestReadToolWithReader_OffsetLimitPassedThrough(t *testing.T) {
	dir := t.TempDir()
	var receivedPath string
	readFn := ReadFileFn(func(_ context.Context, path string) (string, error) {
		receivedPath = path
		return "line1\nline2\nline3\nline4\nline5\n", nil
	})
	tool := NewReadToolWithReader(dir, readFn)
	offset := 2
	limit := 2
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path":   filepath.Join(dir, "file.txt"),
		"offset": float64(offset),
		"limit":  float64(limit),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPath == "" {
		t.Error("readFn was not called")
	}
	text := result.Content[0].Text
	if strings.Contains(text, "line1") {
		t.Error("line1 should be skipped by offset=2")
	}
	if !strings.Contains(text, "line2") {
		t.Errorf("expected line2 in output: %q", text)
	}
}

func TestReadToolWithReader_EmptyPathReturnsError(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "", nil
	})
	tool := NewReadToolWithReader(dir, readFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": "",
	}, nil)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestReadToolWithReader_ReadFnError(t *testing.T) {
	dir := t.TempDir()
	readFn := ReadFileFn(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("delegate read failed")
	})
	tool := NewReadToolWithReader(dir, readFn)
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"path": filepath.Join(dir, "file.txt"),
	}, nil)
	if err == nil {
		t.Error("expected error when readFn returns error")
	}
}
