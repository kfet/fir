package acp

import (
	"context"
	"errors"
	"fmt"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
)

// createAcpTools creates tools with ACP delegation based on client capabilities.
// useClientTerminal: route bash execution through ACP client terminal.
// useClientFs: route read/write/edit file I/O through ACP client fs methods.
// shellCommandPrefix: optional prefix prepended to every bash command.
func (pa *firAgent) createAcpTools(cwd, sessionID string, useClientTerminal, useClientFs bool, shellCommandPrefix string) []agent.AgentTool {
	var readTool agent.AgentTool
	var editTool agent.AgentTool
	var writeTool agent.AgentTool
	if useClientFs {
		readTool = pa.createAcpReadTool(cwd, sessionID)
		editTool = pa.createAcpEditTool(cwd, sessionID)
		writeTool = pa.createAcpWriteTool(cwd, sessionID)
	} else {
		readTool = tools.NewReadTool(cwd)
		editTool = tools.NewEditTool(cwd)
		writeTool = tools.NewWriteTool(cwd)
	}

	var bashTool agent.AgentTool
	if useClientTerminal {
		bashTool = pa.createAcpBashTool(cwd, sessionID, shellCommandPrefix)
	} else {
		bashTool = tools.NewBashToolWithPrefix(cwd, shellCommandPrefix)
	}

	toolList := []agent.AgentTool{
		readTool,
		bashTool,
		editTool,
		writeTool,
		tools.NewGrepTool(cwd),
		tools.NewFindTool(cwd),
		tools.NewLsTool(cwd),
	}
	if useClientTerminal {
		toolList = append(toolList,
			pa.createBashOutputTool(sessionID),
			pa.createBashKillTool(sessionID),
		)
	}
	return toolList
}

// createAcpReadFn returns a ReadFileFn that delegates to the ACP client.
func (pa *firAgent) createAcpReadFn(sessionID string) tools.ReadFileFn {
	return func(ctx context.Context, path string) (string, error) {
		resp, err := pa.conn.ReadTextFile(ctx, acpsdk.ReadTextFileRequest{
			SessionId: acpsdk.SessionId(sessionID),
			Path:      path,
		})
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}

// createAcpWriteFn returns a WriteFileFn that delegates to the ACP client.
func (pa *firAgent) createAcpWriteFn(sessionID string) tools.WriteFileFn {
	return func(ctx context.Context, path, content string) error {
		_, err := pa.conn.WriteTextFile(ctx, acpsdk.WriteTextFileRequest{
			SessionId: acpsdk.SessionId(sessionID),
			Path:      path,
			Content:   content,
		})
		return err
	}
}

// createAcpReadTool creates a read tool delegating to the ACP client.
func (pa *firAgent) createAcpReadTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewReadToolWithReader(cwd, pa.createAcpReadFn(sessionID))
}

// createAcpWriteTool creates a write tool delegating to the ACP client.
func (pa *firAgent) createAcpWriteTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewWriteToolWithWriter(cwd, pa.createAcpWriteFn(sessionID))
}

// createAcpEditTool creates an edit tool delegating file I/O to the ACP client.
func (pa *firAgent) createAcpEditTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewEditToolWithReadWriter(cwd,
		pa.createAcpReadFn(sessionID),
		pa.createAcpWriteFn(sessionID),
	)
}

func (pa *firAgent) createAcpBashTool(cwd, sessionID, shellCommandPrefix string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command. Output truncated to %d lines or %dKB. Set run_in_background for long-running processes.",
				tools.DefaultMaxLines, tools.DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":           map[string]any{"type": "string", "description": "Bash command to execute"},
					"timeout":           map[string]any{"type": "number", "description": "Timeout in seconds (optional)"},
					"run_in_background": map[string]any{"type": "boolean", "description": "Run in background, returns command_id"},
				},
				"required": []string{"command"},
			},
		},
		Label: "bash",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			command, _ := params["command"].(string)
			timeout, _ := params["timeout"].(float64)
			runInBg, _ := params["run_in_background"].(bool)

			// Apply shell command prefix if configured.
			if shellCommandPrefix != "" {
				command = shellCommandPrefix + "\n" + command
			}

			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			if runInBg {
				commandID, err := StartBackgroundCommand(ctx, pa.conn, entry.termState, sessionID, command, cwd, toolCallID)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: fmt.Sprintf("Background command started with ID: %s\nUse bash_output to check output and bash_kill to terminate.", commandID)}},
				}, nil
			}

			// Foreground: use ACP terminal
			result, err := AcpBashExec(ctx, pa.conn, entry.termState, sessionID, toolCallID, command, cwd, int(timeout))
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			text := result.Output
			if text == "" {
				text = "(no output)"
			}
			if result.ExitCode != nil && *result.ExitCode != 0 {
				text += fmt.Sprintf("\n\nCommand exited with code %d", *result.ExitCode)
				return agent.AgentToolResult{}, errors.New(text)
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

func (pa *firAgent) createBashOutputTool(sessionID string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "bash_output",
			Description: "Get the output of a background bash command.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command_id": map[string]any{"type": "string", "description": "ID of the background command"},
				},
				"required": []string{"command_id"},
			},
		},
		Label: "bash output",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			commandID, _ := params["command_id"].(string)
			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			output, isRunning, exitCode, err := GetBackgroundOutput(ctx, pa.conn, entry.termState, sessionID, commandID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			status := "running"
			if !isRunning {
				ec := "nil"
				if exitCode != nil {
					ec = fmt.Sprintf("%d", *exitCode)
				}
				status = fmt.Sprintf("exited (code %s)", ec)
			}
			if output == "" {
				output = "(no output)"
			}
			text := fmt.Sprintf("Status: %s\n\n%s", status, output)

			if !isRunning && exitCode != nil && *exitCode != 0 {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: text}},
				}, nil
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

func (pa *firAgent) createBashKillTool(sessionID string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "bash_kill",
			Description: "Kill a background bash command and return its final output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command_id": map[string]any{"type": "string", "description": "ID of the background command to kill"},
				},
				"required": []string{"command_id"},
			},
		},
		Label: "bash kill",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			commandID, _ := params["command_id"].(string)
			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			output, exitCode, err := KillBackgroundCommand(ctx, pa.conn, entry.termState, sessionID, commandID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if output == "" {
				output = "(no output)"
			}
			ec := "nil"
			if exitCode != nil {
				ec = fmt.Sprintf("%d", *exitCode)
			}
			text := fmt.Sprintf("Command killed (exit code %s)\n\n%s", ec, output)
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}
