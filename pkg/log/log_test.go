package log

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resetLogger() {
	mu.Lock()
	logger = slog.New(discardHandler{})
	mu.Unlock()
}

func TestInit_Disabled(t *testing.T) {
	resetLogger()
	cleanup, err := Init(false, "")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()

	// Should not panic.
	Debug("should be no-op", "key", "value")
	Info("should be no-op")
	Warn("should be no-op")
	Error("should be no-op")
}

func TestInit_Enabled_WritesJSON(t *testing.T) {
	resetLogger()
	path := filepath.Join(t.TempDir(), "debug.log")

	cleanup, err := Init(true, path)
	if err != nil {
		t.Fatal(err)
	}

	Info("hello", "animal", "cat")
	Debug("detail", "n", 42)
	cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %s", len(lines), string(data))
	}

	// Parse first line.
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("line 0 is not valid JSON: %v", err)
	}
	if entry["msg"] != "hello" {
		t.Errorf("expected msg=hello, got %v", entry["msg"])
	}
	if entry["animal"] != "cat" {
		t.Errorf("expected animal=cat, got %v", entry["animal"])
	}
	if _, ok := entry["time"]; !ok {
		t.Error("expected time field")
	}

	// Parse second line.
	var entry2 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &entry2); err != nil {
		t.Fatalf("line 1 is not valid JSON: %v", err)
	}
	if entry2["msg"] != "detail" {
		t.Errorf("expected msg=detail, got %v", entry2["msg"])
	}
	if n, ok := entry2["n"].(float64); !ok || n != 42 {
		t.Errorf("expected n=42, got %v", entry2["n"])
	}
}

func TestInit_BadPath(t *testing.T) {
	resetLogger()
	_, err := Init(true, "/nonexistent/dir/file.log")
	if err == nil {
		t.Fatal("expected error for bad path")
	}
}

func TestInit_AppendsToExistingFile(t *testing.T) {
	resetLogger()
	path := filepath.Join(t.TempDir(), "debug.log")

	// Write some pre-existing content (valid JSON line).
	os.WriteFile(path, []byte(`{"msg":"old"}`+"\n"), 0644)

	cleanup, err := Init(true, path)
	if err != nil {
		t.Fatal(err)
	}
	Info("fresh")
	cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (old + new), got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"old"`) {
		t.Error("old content should be preserved")
	}
	if !strings.Contains(lines[1], `"fresh"`) {
		t.Error("new content should be appended")
	}
}

func TestWith_SubLogger(t *testing.T) {
	resetLogger()
	path := filepath.Join(t.TempDir(), "debug.log")

	cleanup, err := Init(true, path)
	if err != nil {
		t.Fatal(err)
	}

	sub := With("component", "bash")
	sub.Debug("exec", "cmd", "ls")
	cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if entry["component"] != "bash" {
		t.Errorf("expected component=bash, got %v", entry["component"])
	}
	if entry["cmd"] != "ls" {
		t.Errorf("expected cmd=ls, got %v", entry["cmd"])
	}
}

func TestDefaultLogger_NoInit(t *testing.T) {
	// Calling log functions without Init should not panic.
	Debug("no-init", "k", "v")
	Info("no-init")
	Warn("no-init")
	Error("no-init")
	sub := With("x", "y")
	sub.Debug("no-init")
}
