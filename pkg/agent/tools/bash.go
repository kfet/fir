// Ported from: packages/coding-agent/src/core/tools/bash.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// BashToolParams are the parameters for the bash tool.
type BashToolParams struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"` // seconds
}

// DefaultBashTimeout is applied when the agent does not pass an explicit
// timeout. Agents can override (up or down) via the "timeout" parameter.
// It is a var (not const) so tests can shorten it.
var DefaultBashTimeout = 10 * time.Second

// NewBashTool creates the bash tool for the given working directory.
func NewBashTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command in the current working directory (%s/%s). Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds (default 10s if omitted; pass an explicit value to override up or down). Background processes started with `&` (including under `nohup`) are killed when the foreground command exits, so the tool returns promptly instead of waiting on the inherited pipe. Daemons that detach via `setsid` or double-fork (tmux server, sshd, dockerd, etc.) escape this and keep running.",
				runtime.GOOS, runtime.GOARCH, DefaultMaxLines, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Bash command to execute",
					},
					"timeout": map[string]any{
						"type":        "number",
						"description": "Timeout in seconds. Defaults to 10s if omitted; pass an explicit value to override (e.g. 60 for slower commands).",
					},
				},
				"required": []string{"command"},
			},
		},
		Label: "bash",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			command, _ := params["command"].(string)
			if command == "" {
				return agent.AgentToolResult{}, fmt.Errorf("command is required")
			}

			var timeout time.Duration
			if t, ok := params["timeout"].(float64); ok && t > 0 {
				timeout = time.Duration(t * float64(time.Second))
			} else {
				timeout = DefaultBashTimeout
			}

			return executeBash(ctx, command, cwd, timeout)
		},
	}
}

// NewBashToolWithPrefix creates a bash tool that prepends a shell command prefix
// (e.g., "source /etc/profile") before each command.
func NewBashToolWithPrefix(cwd, commandPrefix string) agent.AgentTool {
	t := NewBashTool(cwd)
	if commandPrefix == "" {
		return t
	}
	orig := t.Execute
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		if cmd, ok := params["command"].(string); ok {
			p := make(map[string]any, len(params))
			for k, v := range params {
				p[k] = v
			}
			p["command"] = commandPrefix + "\n" + cmd
			return orig(ctx, toolCallID, p, onUpdate)
		}
		return orig(ctx, toolCallID, params, onUpdate)
	}
	return t
}

// truncateForLog truncates a string for log output.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// executeBash runs a bash command and returns the result.
func executeBash(ctx context.Context, command, cwd string, timeout time.Duration) (agent.AgentToolResult, error) {
	firlog.Debug("bash exec", "command", truncateForLog(command, 200), "cwd", cwd, "timeout", timeout)
	start := time.Now()
	// Verify cwd exists
	if _, err := os.Stat(cwd); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("working directory does not exist: %s", cwd)
	}

	// Apply timeout if specified
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Create command. We deliberately do NOT use exec.CommandContext: we need
	// to manage the process lifecycle ourselves so we can kill the entire
	// process group as soon as the foreground bash process exits, reaping any
	// backgrounded children that would otherwise hold the stdout pipe open
	// (e.g. `(sleep 30; echo done) &`). Daemons that detach via setsid (tmux
	// server, sshd, etc.) escape the group and survive — that is the whole
	// point of setsid.
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = AppendColorEnv(os.Environ())
	cmd.Env = append(cmd.Env, "GIT_EDITOR=true")

	// Run bash in its own process group so we can kill the entire group on
	// exit/cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Use our own pipe for stdout/stderr so we can drain it after killpg.
	pr, pw, err := os.Pipe()
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return agent.AgentToolResult{}, err
	}
	// Close the parent's copy of the write end; only children hold it now.
	pw.Close()

	// Drain pipe into buffer.
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, pr)
		pr.Close()
		close(drained)
	}()

	// Watch ctx for cancellation/timeout; kill the process group if it fires
	// before bash exits naturally.
	mainExited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-mainExited:
		}
	}()

	// Wait for bash itself to exit (does not wait for backgrounded children).
	state, waitErr := cmd.Process.Wait()
	close(mainExited)

	// Reap any orphaned children still in the group (backgrounded `&` jobs
	// that inherit the pipe; without this they would block the drain
	// indefinitely). ESRCH is expected when no group members remain.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	// Wait for the drain to finish naturally. Bash flushed and exited before
	// Wait returned, and after killpg above all group members are gone, so
	// io.Copy should hit EOF promptly. If a just-forked descendant of a
	// backgrounded subshell still briefly holds the write end past our
	// killpg (a kernel race observed on macOS where killpg returns success
	// but the pipe stays open), fall back to force-closing pr after a short
	// grace period. We must NOT close pr eagerly: under Linux + race
	// detector the close can win the race against the drain's first Read,
	// truncating the output to empty. Tail output from such held-open
	// descendants may still be discarded — that is the documented trade-off.
	select {
	case <-drained:
	case <-time.After(50 * time.Millisecond):
		_ = pr.Close()
		<-drained
	}

	// Synthesise an exec.ExitError on non-zero exit so downstream code keeps
	// working (cmd.Wait normally does this for us).
	err = waitErr
	if err == nil && state != nil && !state.Success() {
		err = &exec.ExitError{ProcessState: state}
	}

	output := buf.String()

	// Keep raw output (with ANSI) for display, strip for LLM context
	rawOutput := output
	output = StripAnsi(output)

	// Apply tail truncation
	truncResult := TruncateTail(output, TruncationOptions{})
	outputText := truncResult.Content
	if outputText == "" {
		outputText = "(no output)"
	}

	// Handle truncation notice
	var fullOutputPath string
	if truncResult.Truncated {
		// Write full output to temp file
		tmpFile, tmpErr := os.CreateTemp("", "fir-bash-*.log")
		if tmpErr == nil {
			tmpFile.WriteString(output)
			tmpFile.Close()
			fullOutputPath = tmpFile.Name()
		}

		startLine := truncResult.TotalLines - truncResult.OutputLines + 1
		endLine := truncResult.TotalLines

		if truncResult.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
				startLine, endLine, truncResult.TotalLines, fullOutputPath)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%dKB limit). Full output: %s]",
				startLine, endLine, truncResult.TotalLines, DefaultMaxBytes/1024, fullOutputPath)
		}
	}

	if err != nil {
		// Check for specific error types
		if ctx.Err() == context.DeadlineExceeded {
			if output != "" {
				outputText += "\n\n"
			}
			outputText += fmt.Sprintf("Command timed out after %.0f seconds", timeout.Seconds())
			return agent.AgentToolResult{}, errors.New(outputText)
		}
		if ctx.Err() == context.Canceled {
			if output != "" {
				outputText += "\n\n"
			}
			outputText += "Command aborted"
			return agent.AgentToolResult{}, errors.New(outputText)
		}

		// Process exited with non-zero code
		if exitErr, ok := err.(*exec.ExitError); ok {
			firlog.Debug("bash done", "exitCode", exitErr.ExitCode(), "outputLen", len(output), "elapsed", time.Since(start))
			outputText += fmt.Sprintf("\n\nCommand exited with code %d", exitErr.ExitCode())
			return agent.AgentToolResult{}, errors.New(outputText)
		}

		firlog.Warn("bash error", "err", err, "elapsed", time.Since(start))
		return agent.AgentToolResult{}, err
	}

	firlog.Debug("bash done", "exitCode", 0, "outputLen", len(output), "truncated", truncResult.Truncated, "elapsed", time.Since(start))

	details := map[string]any{}
	if fullOutputPath != "" {
		details["fullOutputPath"] = fullOutputPath
	}

	// Store raw output (with ANSI colors) for TUI display.
	// Truncate to same line budget as the LLM output.
	rawTrunc := TruncateTail(rawOutput, TruncationOptions{})
	details["rawOutput"] = rawTrunc.Content

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: outputText},
		},
		Details: details,
	}, nil
}
