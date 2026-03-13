// Ported from: packages/coding-agent/src/core/tools/ls.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

const lsDefaultLimit = 500

// LsToolParams are the parameters for the ls tool.
type LsToolParams struct {
	Path  string `json:"path,omitempty"`
	Limit *int   `json:"limit,omitempty"`
}

// LsToolDetails contains details about the ls result.
type LsToolDetails struct {
	Truncation        *TruncationResult `json:"truncation,omitempty"`
	EntryLimitReached *int              `json:"entryLimitReached,omitempty"`
}

// NewLsTool creates the ls tool for the given working directory.
func NewLsTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "ls",
			Description: fmt.Sprintf(
				"List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. "+
					"Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).",
				lsDefaultLimit, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to list (default: current directory)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": fmt.Sprintf("Maximum number of entries to return (default: %d)", lsDefaultLimit),
					},
				},
			},
		},
		Label: "ls",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			path := "."
			if p, ok := params["path"].(string); ok && p != "" {
				path = p
			}

			effectiveLimit := lsDefaultLimit
			if l, ok := params["limit"].(float64); ok {
				effectiveLimit = int(l)
				if effectiveLimit < 1 {
					effectiveLimit = 1
				}
			}

			dirPath := ResolveToCwd(path, cwd)

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Check if path exists
			info, err := os.Stat(dirPath)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("path not found: %s", dirPath)
			}

			if !info.IsDir() {
				return agent.AgentToolResult{}, fmt.Errorf("not a directory: %s", dirPath)
			}

			// Read directory entries
			entries, err := os.ReadDir(dirPath)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("cannot read directory: %w", err)
			}

			// Sort alphabetically (case-insensitive)
			sort.Slice(entries, func(i, j int) bool {
				return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
			})

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Format entries
			var results []string
			entryLimitReached := false

			for _, entry := range entries {
				if len(results) >= effectiveLimit {
					entryLimitReached = true
					break
				}

				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				} else {
					// Check if it's a symlink to a directory
					fullPath := filepath.Join(dirPath, entry.Name())
					if target, err := os.Stat(fullPath); err == nil && target.IsDir() {
						name += "/"
					}
				}
				results = append(results, name)
			}

			if len(results) == 0 {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{
						{Type: "text", Text: "(empty directory)"},
					},
				}, nil
			}

			// Apply byte truncation
			rawOutput := strings.Join(results, "\n")
			truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: math.MaxInt})

			output := truncation.Content
			var details *LsToolDetails

			var notices []string
			if entryLimitReached {
				notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", effectiveLimit, effectiveLimit*2))
			}
			if truncation.Truncated {
				notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
			}

			if len(notices) > 0 {
				output += "\n\n[" + strings.Join(notices, ". ") + "]"
			}

			if entryLimitReached || truncation.Truncated {
				limit := effectiveLimit
				details = &LsToolDetails{}
				if entryLimitReached {
					details.EntryLimitReached = &limit
				}
				if truncation.Truncated {
					details.Truncation = &truncation
				}
			}

			result := agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: "text", Text: output},
				},
			}
			if details != nil {
				result.Details = details
			}

			return result, nil
		},
	}
}
