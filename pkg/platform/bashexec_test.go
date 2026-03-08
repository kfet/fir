package platform

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent/tools"
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

func TestExecuteBash_OnChunkPreservesANSI(t *testing.T) {
	var chunks []string
	opts := &BashExecutorOptions{
		OnChunk: func(chunk string) {
			chunks = append(chunks, chunk)
		},
	}

	ctx := context.Background()
	// printf emits raw ANSI regardless of pipe
	result, err := ExecuteBash(ctx, `printf '\033[32mgreen\033[0m'`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d", result.ExitCode)
	}

	combined := strings.Join(chunks, "")
	if !strings.Contains(combined, "\x1b[32m") {
		t.Errorf("OnChunk should preserve ANSI codes, got %q", combined)
	}
	if !strings.Contains(combined, "green") {
		t.Errorf("OnChunk should contain text, got %q", combined)
	}

	// But the stored output (for LLM) should be stripped
	if strings.Contains(result.Output, "\x1b") {
		t.Errorf("BashResult.Output should be ANSI-stripped, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "green") {
		t.Errorf("BashResult.Output should contain text, got %q", result.Output)
	}
}

func TestExecuteBash_OnChunkInjectsColorEnv(t *testing.T) {
	var chunks []string
	opts := &BashExecutorOptions{
		OnChunk: func(chunk string) {
			chunks = append(chunks, chunk)
		},
	}

	ctx := context.Background()
	result, err := ExecuteBash(ctx, `echo "COLOR=$CLICOLOR CLI=$CLICOLOR_FORCE FORCE=$FORCE_COLOR"`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d", result.ExitCode)
	}
	if !strings.Contains(result.Output, "COLOR=1") {
		t.Errorf("expected CLICOLOR=1 in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "CLI=3") {
		t.Errorf("expected CLICOLOR_FORCE=3 in output, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "FORCE=1") {
		t.Errorf("expected FORCE_COLOR=1 in output, got %q", result.Output)
	}
}

func TestExecuteBash_OnChunkRespectsExistingColorEnv(t *testing.T) {
	t.Setenv("CLICOLOR", "0")
	t.Setenv("CLICOLOR_FORCE", "0")
	t.Setenv("FORCE_COLOR", "0")

	var chunks []string
	opts := &BashExecutorOptions{
		OnChunk: func(chunk string) {
			chunks = append(chunks, chunk)
		},
	}

	ctx := context.Background()
	result, err := ExecuteBash(ctx, `echo "COLOR=$CLICOLOR CLI=$CLICOLOR_FORCE FORCE=$FORCE_COLOR"`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "COLOR=0") {
		t.Errorf("should respect existing CLICOLOR=0, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "CLI=0") {
		t.Errorf("should respect existing CLICOLOR_FORCE=0, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "FORCE=0") {
		t.Errorf("should respect existing FORCE_COLOR=0, got %q", result.Output)
	}
}

func TestSanitizeBinaryOutput_PreservesESC(t *testing.T) {
	// ESC (\x1b) should be preserved for ANSI sequences
	input := "hello\x1b[32mgreen\x1b[0m\x01\x02world"
	got := sanitizeBinaryOutput(input)
	want := "hello\x1b[32mgreen\x1b[0m??world"
	if got != want {
		t.Errorf("sanitizeBinaryOutput(%q) = %q, want %q", input, got, want)
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
		got := tools.StripAnsi(tt.input)
		if got != tt.want {
			t.Errorf("StripAnsi(%q) = %q, want %q", tt.input, got, tt.want)
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
