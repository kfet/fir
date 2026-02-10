package core

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecuteBash_SimpleCommand(t *testing.T) {
	ctx := context.Background()
	result, err := ExecuteBash(ctx, "echo hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("output = %q, want 'hello'", result.Output)
	}
	if result.Cancelled {
		t.Error("should not be cancelled")
	}
}

func TestExecuteBash_ExitCode(t *testing.T) {
	ctx := context.Background()
	result, err := ExecuteBash(ctx, "exit 42", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 42 {
		t.Errorf("exit code = %d, want 42", result.ExitCode)
	}
}

func TestExecuteBash_StderrCapture(t *testing.T) {
	ctx := context.Background()
	result, err := ExecuteBash(ctx, "echo error >&2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "error") {
		t.Errorf("output = %q, should contain stderr", result.Output)
	}
}

func TestExecuteBash_Cancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := ExecuteBash(ctx, "sleep 10", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cancelled {
		t.Error("expected cancelled")
	}
}

func TestExecuteBash_OnChunk(t *testing.T) {
	var chunks []string
	opts := &BashExecutorOptions{
		OnChunk: func(chunk string) {
			chunks = append(chunks, chunk)
		},
	}

	ctx := context.Background()
	result, err := ExecuteBash(ctx, "echo line1; echo line2", opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d", result.ExitCode)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk")
	}
	combined := strings.Join(chunks, "")
	if !strings.Contains(combined, "line1") || !strings.Contains(combined, "line2") {
		t.Errorf("chunks = %q", combined)
	}
}

func TestExecuteBash_MultiLine(t *testing.T) {
	ctx := context.Background()
	result, err := ExecuteBash(ctx, "echo first && echo second && echo third", nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

func TestExecuteBash_EmptyOutput(t *testing.T) {
	ctx := context.Background()
	result, err := ExecuteBash(ctx, "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestExecuteBashSimple(t *testing.T) {
	output, exitCode, err := ExecuteBashSimple(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d", exitCode)
	}
	if !strings.Contains(output, "hi") {
		t.Errorf("output = %q", output)
	}
}

func TestExecuteBashCapture(t *testing.T) {
	output, err := ExecuteBashCapture("echo captured")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "captured") {
		t.Errorf("output = %q", output)
	}
}

func TestStripAnsi(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"no ansi", "no ansi"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripAnsi(tt.input)
		if got != tt.want {
			t.Errorf("stripAnsi(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeBinaryOutput(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello\nworld", "hello\nworld"},
		{"tab\there", "tab\there"},
		{"binary\x01\x02here", "binary??here"},
		{"unicode: 日本語", "unicode: 日本語"},
	}
	for _, tt := range tests {
		got := sanitizeBinaryOutput(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeBinaryOutput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExecuteBash_LargeOutput(t *testing.T) {
	// Generate output larger than default max bytes
	ctx := context.Background()
	cmd := "for i in $(seq 1 5000); do echo 'line number '$i' with some padding text to make it longer xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'; done"
	result, err := ExecuteBash(ctx, cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d", result.ExitCode)
	}
	// Output should contain something
	if len(result.Output) == 0 {
		t.Error("expected non-empty output")
	}
	// If it was large enough, should have created temp file
	if result.FullOutputPath != "" {
		// Verify temp file exists
		if _, err := os.Stat(result.FullOutputPath); err != nil {
			t.Errorf("temp file %s should exist: %v", result.FullOutputPath, err)
		}
		// Clean up
		os.Remove(result.FullOutputPath)
	}
}
