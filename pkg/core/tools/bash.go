// Ported from: packages/coding-agent/src/core/tools/bash.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

// BashToolParams are the parameters for the bash tool.
type BashToolParams struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"` // seconds
}

// NewBashTool creates the bash tool for the given working directory.
func NewBashTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
				DefaultMaxLines, DefaultMaxBytes/1024,
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
						"description": "Timeout in seconds (optional, no default timeout)",
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
			}

			return executeBash(ctx, command, cwd, timeout)
		},
	}
}

// executeBash runs a bash command and returns the result.
func executeBash(ctx context.Context, command, cwd string, timeout time.Duration) (agent.AgentToolResult, error) {
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

	// Create command
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	// Use process group for cleanup
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture output
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()

	output := buf.String()

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
		tmpFile, tmpErr := os.CreateTemp("", "pi-bash-*.log")
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
			outputText += fmt.Sprintf("\n\nCommand exited with code %d", exitErr.ExitCode())
			return agent.AgentToolResult{}, errors.New(outputText)
		}

		return agent.AgentToolResult{}, err
	}

	details := map[string]any{}
	if fullOutputPath != "" {
		details["fullOutputPath"] = fullOutputPath
	}

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: outputText},
		},
		Details: details,
	}, nil
}

