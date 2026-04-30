package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBashTool_Echo(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo hello",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if !strings.Contains(result.Content[0].Text, "hello") {
		t.Errorf("output = %q, want contains hello", result.Content[0].Text)
	}
}

func TestBashTool_Stderr(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo error >&2",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "error") {
		t.Errorf("output = %q, want contains error", result.Content[0].Text)
	}
}

func TestBashTool_ExitCode(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "exit 42",
	}, nil)
	if err == nil {
		t.Fatal("expected error for non-zero exit code")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error = %q, want contains 42", err.Error())
	}
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "sleep 10",
		"timeout": float64(1),
	}, nil)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want contains 'timed out'", err.Error())
	}
}

func TestBashTool_Abort(t *testing.T) {
	tool := NewBashTool(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := tool.Execute(ctx, "call-1", map[string]any{
		"command": "sleep 10",
	}, nil)
	if err == nil {
		t.Fatal("expected error for abort")
	}
}

// TestBashTool_AbortWithChildren verifies that cancelling the context kills the
// entire process group, not just the bash process.  Without the process-group
// fix, bash child processes keep the stdout/stderr pipe open after bash is
// killed, causing cmd.Wait() (and therefore Execute) to block until all
// children exit naturally.
func TestBashTool_AbortWithChildren(t *testing.T) {
	tool := NewBashTool(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// "sleep 30 & sleep 30" forces bash to fork children rather than exec()
	// into sleep, so without the process-group kill the pipe would stay open
	// for ~30 s after bash is killed.
	_, err := tool.Execute(ctx, "call-1", map[string]any{
		"command": "sleep 30 & sleep 30",
	}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for abort")
	}
	// Should return well within a second; 5 s is a generous upper bound.
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %v after cancel — child processes not killed (pipe held open)", elapsed)
	}
}

// TestBashTool_BackgroundChildHoldsPipe verifies that a backgrounded subshell
// which inherits the stdout pipe does not block the tool. Without the
// "killpg-after-bash-exits" reaping, this would block until the background
// `sleep` finishes (~30s), even though bash itself exited immediately.
func TestBashTool_BackgroundChildHoldsPipe(t *testing.T) {
	tool := NewBashTool(t.TempDir())

	start := time.Now()
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "(sleep 30; echo done) &\necho started",
	}, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %v — backgrounded child held pipe open", elapsed)
	}
}

func TestBashTool_NoOutput(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "true",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "(no output)") {
		t.Errorf("output = %q, want contains '(no output)'", result.Content[0].Text)
	}
}

func TestBashTool_MissingCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{}, nil)
	if err == nil {
		t.Error("expected error for missing command")
	}
}

func TestBashTool_CwdDoesNotExist(t *testing.T) {
	tool := NewBashTool("/nonexistent/path")
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo hello",
	}, nil)
	if err == nil {
		t.Error("expected error for nonexistent cwd")
	}
}

func TestBashTool_MultilineOutput(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo line1; echo line2; echo line3",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := result.Content[0].Text
	if !strings.Contains(output, "line1") || !strings.Contains(output, "line3") {
		t.Errorf("output = %q, want all lines", output)
	}
}

func TestBashTool_Pwd(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool(dir)
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "pwd",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The output should contain the temp dir (may have symlinks resolved)
	if !strings.Contains(result.Content[0].Text, "tmp") && !strings.Contains(result.Content[0].Text, "temp") && !strings.Contains(result.Content[0].Text, "var") {
		t.Errorf("pwd output = %q, doesn't look like temp dir", result.Content[0].Text)
	}
}

func TestBashToolWithPrefix_PrependedCorrectly(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashToolWithPrefix(dir, "export MY_PREFIX_VAR=hello")
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo $MY_PREFIX_VAR",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content[0].Text, "hello") {
		t.Errorf("prefix not applied: output = %q", result.Content[0].Text)
	}
}

func TestBashToolWithPrefix_EmptyPrefixReturnsOriginal(t *testing.T) {
	dir := t.TempDir()
	orig := NewBashTool(dir)
	withPrefix := NewBashToolWithPrefix(dir, "")
	// Both should produce the same output for the same command.
	cmd := map[string]any{"command": "echo same"}
	r1, _ := orig.Execute(context.Background(), "c1", cmd, nil)
	r2, _ := withPrefix.Execute(context.Background(), "c2", cmd, nil)
	if len(r1.Content) == 0 || len(r2.Content) == 0 {
		t.Fatal("expected content in both results")
	}
	if r1.Content[0].Text != r2.Content[0].Text {
		t.Errorf("empty prefix changed output: orig=%q, prefixed=%q", r1.Content[0].Text, r2.Content[0].Text)
	}
}

func TestBashToolWithPrefix_NonStringCommandFallsThrough(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashToolWithPrefix(dir, "echo PREFIX")
	// Non-string command param: the original executor returns an error.
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"command": 42, // not a string
	}, nil)
	// The original bash tool returns an error for empty command.
	if err == nil {
		t.Error("expected error for non-string command param")
	}
}
