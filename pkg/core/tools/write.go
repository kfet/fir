// Ported from: packages/coding-agent/src/core/tools/write.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// WriteToolParams are the parameters for the write tool.
type WriteToolParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// NewWriteTool creates the write tool for the given working directory.
func NewWriteTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "write",
			Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to write (relative or absolute)",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write to the file",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		Label: "write",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			path, _ := params["path"].(string)
			content, _ := params["content"].(string)

			if path == "" {
				return agent.AgentToolResult{}, fmt.Errorf("path is required")
			}

			absolutePath := ResolveToCwd(path, cwd)

			firlog.Debug("write file", "path", path, "contentLen", len(content))

			// Check context cancellation
			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Create parent directories
			dir := filepath.Dir(absolutePath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("failed to create directory %s: %w", dir, err)
			}

			// Check context cancellation before write
			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Write the file
			if err := os.WriteFile(absolutePath, []byte(content), 0644); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("failed to write file %s: %w", path, err)
			}

			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: "text", Text: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path)},
				},
			}, nil
		},
	}
}

// WriteFileFn is a function that writes content to a file path.
// Used for ACP client delegation.
type WriteFileFn func(ctx context.Context, path, content string) error

// NewWriteToolWithWriter creates a write tool that delegates file writes to writeFn.
func NewWriteToolWithWriter(cwd string, writeFn WriteFileFn) agent.AgentTool {
	t := NewWriteTool(cwd)
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		path, _ := params["path"].(string)
		content, _ := params["content"].(string)
		if path == "" {
			return agent.AgentToolResult{}, fmt.Errorf("path is required")
		}
		absolutePath := ResolveToCwd(path, cwd)
		if ctx.Err() != nil {
			return agent.AgentToolResult{}, ctx.Err()
		}
		if err := writeFn(ctx, absolutePath, content); err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("failed to write file %s: %w", path, err)
		}
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{
				{Type: "text", Text: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path)},
			},
		}, nil
	}
	return t
}
