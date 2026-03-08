// Ported from: packages/coding-agent/src/core/tools/find.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

const findDefaultLimit = 1000

// FindToolParams are the parameters for the find tool.
type FindToolParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Limit   *int   `json:"limit,omitempty"`
}

// FindToolDetails contains details about the find result.
type FindToolDetails struct {
	Truncation         *TruncationResult `json:"truncation,omitempty"`
	ResultLimitReached *int              `json:"resultLimitReached,omitempty"`
}

// NewFindTool creates the find tool for the given working directory.
func NewFindTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "find",
			Description: fmt.Sprintf(
				"Search for files by glob pattern. Returns matching file paths relative to the search directory. "+
					"Respects .gitignore. Output is truncated to %d results or %dKB (whichever is hit first).",
				findDefaultLimit, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in (default: current directory)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": fmt.Sprintf("Maximum number of results (default: %d)", findDefaultLimit),
					},
				},
				"required": []string{"pattern"},
			},
		},
		Label: "find",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			pattern, _ := params["pattern"].(string)
			if pattern == "" {
				return agent.AgentToolResult{}, fmt.Errorf("pattern is required")
			}

			searchDir := "."
			if p, ok := params["path"].(string); ok && p != "" {
				searchDir = p
			}

			effectiveLimit := findDefaultLimit
			if l, ok := params["limit"].(float64); ok {
				effectiveLimit = int(l)
				if effectiveLimit < 1 {
					effectiveLimit = 1
				}
			}

			searchPath := ResolveToCwd(searchDir, cwd)

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Check if path exists
			if _, err := os.Stat(searchPath); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("path not found: %s", searchPath)
			}

			// Try to use fd first, fall back to find
			output, err := runFindCommand(ctx, pattern, searchPath, effectiveLimit)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			output = strings.TrimSpace(output)
			if output == "" {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{
						{Type: "text", Text: "No files found matching pattern"},
					},
				}, nil
			}

			// Relativize paths
			lines := strings.Split(output, "\n")
			var relativized []string
			for _, rawLine := range lines {
				line := strings.TrimRight(strings.TrimSpace(rawLine), "\r")
				if line == "" {
					continue
				}

				hadTrailingSlash := strings.HasSuffix(line, "/") || strings.HasSuffix(line, "\\")
				rel := line
				if strings.HasPrefix(line, searchPath) {
					rel = line[len(searchPath):]
					rel = strings.TrimPrefix(rel, "/")
					rel = strings.TrimPrefix(rel, "\\")
				} else {
					var err error
					rel, err = filepath.Rel(searchPath, line)
					if err != nil {
						rel = filepath.Base(line)
					}
				}

				if hadTrailingSlash && !strings.HasSuffix(rel, "/") {
					rel += "/"
				}

				relativized = append(relativized, rel)
			}

			resultLimitReached := len(relativized) >= effectiveLimit
			rawOutput := strings.Join(relativized, "\n")
			truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: math.MaxInt})

			resultOutput := truncation.Content
			var details *FindToolDetails

			var notices []string
			if resultLimitReached {
				notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more, or refine pattern", effectiveLimit, effectiveLimit*2))
			}
			if truncation.Truncated {
				notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
			}

			if len(notices) > 0 {
				resultOutput += "\n\n[" + strings.Join(notices, ". ") + "]"
			}

			if resultLimitReached || truncation.Truncated {
				details = &FindToolDetails{}
				if resultLimitReached {
					details.ResultLimitReached = &effectiveLimit
				}
				if truncation.Truncated {
					details.Truncation = &truncation
				}
			}

			result := agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: "text", Text: resultOutput},
				},
			}
			if details != nil {
				result.Details = details
			}

			return result, nil
		},
	}
}

// runFindCommand tries fd first, then falls back to system find.
func runFindCommand(ctx context.Context, pattern, searchPath string, limit int) (string, error) {
	// Try fd first
	fdPath, err := exec.LookPath("fd")
	if err == nil {
		args := []string{
			"--glob", "--color=never", "--hidden",
			"--max-results", fmt.Sprintf("%d", limit),
			pattern, searchPath,
		}
		cmd := exec.CommandContext(ctx, fdPath, args...)
		out, err := cmd.Output()
		if err != nil {
			// fd returns exit code 1 for no matches, which is not an error
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return "", nil
			}
			// If fd fails, fall back to find
		} else {
			return string(out), nil
		}
	}

	// Fall back to system find with -name glob
	args := []string{searchPath, "-name", pattern, "-not", "-path", "*/.git/*", "-not", "-path", "*/node_modules/*"}
	cmd := exec.CommandContext(ctx, "find", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("find failed: %w", err)
	}

	// Apply limit
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}

	return strings.Join(lines, "\n"), nil
}
