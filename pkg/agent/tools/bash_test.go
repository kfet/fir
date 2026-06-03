package tools

import (
	"context"
	"os"
	"path/filepath"
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

// TestBashTool_DefaultTimeout verifies that omitting the timeout parameter
// applies DefaultBashTimeout rather than hanging forever.
func TestBashTool_DefaultTimeout(t *testing.T) {
	// Temporarily shorten the default so the test stays fast.
	orig := DefaultBashTimeout
	t.Cleanup(func() { DefaultBashTimeout = orig })
	DefaultBashTimeout = 200 * time.Millisecond

	tool := NewBashTool(t.TempDir())
	start := time.Now()
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "sleep 10",
	}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error when no timeout passed")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want contains 'timed out'", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %v — default timeout not applied", elapsed)
	}
}

// TestBashTool_ExplicitTimeoutOverridesDefault verifies that the agent can
// pass an explicit timeout larger than the default.
func TestBashTool_ExplicitTimeoutOverridesDefault(t *testing.T) {
	orig := DefaultBashTimeout
	t.Cleanup(func() { DefaultBashTimeout = orig })
	DefaultBashTimeout = 50 * time.Millisecond

	tool := NewBashTool(t.TempDir())
	// Sleep 300ms, but pass an explicit 5s timeout > default 50ms.
	// Should succeed, not time out.
	_, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "sleep 0.3",
		"timeout": float64(5),
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error with explicit timeout override: %v", err)
	}
}

func TestBashTool_Abort(t *testing.T) {
	tool := NewBashTool(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
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
		time.Sleep(20 * time.Millisecond)
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
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "(sleep 30; echo done) &\necho started",
	}, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %v — backgrounded child held pipe open", elapsed)
	}
	// Foreground bash output must still be captured — the drain must not
	// race-discard "started\n" when we force-close the read end.
	if got := result.Content[0].Text; !strings.Contains(got, "started") {
		t.Errorf("output = %q, want contains 'started' (foreground output lost)", got)
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
	// When the cwd does not exist, bash must not run the command in some other
	// directory. It returns a clear error result and does NOT execute.
	tool := NewBashTool("/nonexistent/path")
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo hello",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError result for nonexistent cwd")
	}
	out := result.Content[0].Text
	if strings.Contains(out, "hello") {
		t.Errorf("command was executed despite missing cwd: %q", out)
	}
	if !strings.Contains(out, "no longer exists") {
		t.Errorf("output = %q, want an explanatory notice", out)
	}
}

func TestBashTool_CwdDeletedAtRuntime(t *testing.T) {
	// Simulate a worktree/temp dir being removed out from under a live
	// session: the cwd was valid when the tool was constructed but is gone
	// by the time the command runs. The command must NOT run in a surviving
	// ancestor — that could trash real files the agent was never pointed at.
	parent := t.TempDir()
	gone := filepath.Join(parent, "worktree")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := NewBashTool(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove cwd: %v", err)
	}

	// A destructive relative-path command must not touch the parent.
	canary := filepath.Join(parent, "canary.txt")
	if err := os.WriteFile(canary, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write canary: %v", err)
	}
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "rm -f *.txt; pwd",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError result after cwd deleted")
	}
	if !strings.Contains(result.Content[0].Text, "no longer exists") {
		t.Errorf("output = %q, want an explanatory notice", result.Content[0].Text)
	}
	if _, statErr := os.Stat(canary); statErr != nil {
		t.Errorf("canary file was deleted — command ran in surviving ancestor: %v", statErr)
	}
}

func TestBashTool_CwdParamOverride(t *testing.T) {
	// The cwd parameter runs the command in a specified directory instead of
	// the session default.
	sessionDir := t.TempDir()
	otherDir := t.TempDir()
	tool := NewBashTool(sessionDir)
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "pwd",
		"cwd":     otherDir,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %q", result.Content[0].Text)
	}
	// macOS reports /private-prefixed temp paths, so match on the basename.
	if !strings.Contains(result.Content[0].Text, filepath.Base(otherDir)) {
		t.Errorf("output = %q, want pwd under override dir %q", result.Content[0].Text, otherDir)
	}
}

func TestBashTool_CwdParamRecoversDeletedSessionDir(t *testing.T) {
	// When the session dir is gone, passing an existing cwd recovers cleanly
	// without resorting to a `cd` prefix.
	gone := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := NewBashTool(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove cwd: %v", err)
	}
	alive := t.TempDir()
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{
		"command": "echo recovered",
		"cwd":     alive,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %q", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "recovered") {
		t.Errorf("output = %q, want command to run via cwd override", result.Content[0].Text)
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

func TestBashToolWithPrefix_ForwardsCwd(t *testing.T) {
	// The prefix wrapper (used by ACP mode) must forward the cwd override
	// through its param copy, not drop it.
	sessionDir := t.TempDir()
	otherDir := t.TempDir()
	tool := NewBashToolWithPrefix(sessionDir, "export MY_PREFIX_VAR=hi")
	result, err := tool.Execute(context.Background(), "c1", map[string]any{
		"command": "pwd; echo $MY_PREFIX_VAR",
		"cwd":     otherDir,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := result.Content[0].Text
	if !strings.Contains(out, "hi") {
		t.Errorf("prefix not applied with cwd override: %q", out)
	}
	if !strings.Contains(out, filepath.Base(otherDir)) {
		t.Errorf("cwd override not forwarded through prefix wrapper: %q", out)
	}
}
