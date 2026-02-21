// Ported from: packages/coding-agent/src/core/tools/grep.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

const grepDefaultLimit = 100

// GrepToolParams are the parameters for the grep tool.
type GrepToolParams struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Literal    bool   `json:"literal,omitempty"`
	Context    int    `json:"context,omitempty"`
	Limit      *int   `json:"limit,omitempty"`
}

// GrepToolDetails contains details about the grep result.
type GrepToolDetails struct {
	Truncation        *TruncationResult `json:"truncation,omitempty"`
	MatchLimitReached *int              `json:"matchLimitReached,omitempty"`
	LinesTruncated    bool              `json:"linesTruncated,omitempty"`
}

// NewGrepTool creates the grep tool for the given working directory.
func NewGrepTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "grep",
			Description: fmt.Sprintf(
				"Search file contents for a pattern. Returns matching lines with file paths and line numbers. "+
					"Respects .gitignore. Output is truncated to %d matches or %dKB (whichever is hit first). "+
					"Long lines are truncated to %d chars.",
				grepDefaultLimit, DefaultMaxBytes/1024, GrepMaxLineLength,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Search pattern (regex or literal string)",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory or file to search (default: current directory)",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'",
					},
					"ignoreCase": map[string]any{
						"type":        "boolean",
						"description": "Case-insensitive search (default: false)",
					},
					"literal": map[string]any{
						"type":        "boolean",
						"description": "Treat pattern as literal string instead of regex (default: false)",
					},
					"context": map[string]any{
						"type":        "number",
						"description": "Number of lines to show before and after each match (default: 0)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": fmt.Sprintf("Maximum number of matches to return (default: %d)", grepDefaultLimit),
					},
				},
				"required": []string{"pattern"},
			},
		},
		Label: "grep",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			pattern, _ := params["pattern"].(string)
			if pattern == "" {
				return agent.AgentToolResult{}, fmt.Errorf("pattern is required")
			}

			searchDir := "."
			if p, ok := params["path"].(string); ok && p != "" {
				searchDir = p
			}

			glob, _ := params["glob"].(string)
			ignoreCase, _ := params["ignoreCase"].(bool)
			literal, _ := params["literal"].(bool)

			contextLines := 0
			if c, ok := params["context"].(float64); ok && c > 0 {
				contextLines = int(c)
			}

			effectiveLimit := grepDefaultLimit
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
			info, err := os.Stat(searchPath)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("path not found: %s", searchPath)
			}
			isDirectory := info.IsDir()

			// Try ripgrep first, fall back to grep
			rgPath, err := exec.LookPath("rg")
			if err != nil {
				// Fall back to grep
				return grepFallback(ctx, pattern, searchPath, isDirectory, ignoreCase, literal, glob, effectiveLimit)
			}

			return grepWithRipgrep(ctx, rgPath, pattern, searchPath, isDirectory, ignoreCase, literal, glob, contextLines, effectiveLimit)
		},
	}
}

func grepWithRipgrep(ctx context.Context, rgPath, pattern, searchPath string, isDirectory, ignoreCase, literal bool, glob string, contextLines, limit int) (agent.AgentToolResult, error) {
	args := []string{"--json", "--line-number", "--color=never", "--hidden"}

	if ignoreCase {
		args = append(args, "--ignore-case")
	}
	if literal {
		args = append(args, "--fixed-strings")
	}
	if glob != "" {
		args = append(args, "--glob", glob)
	}

	args = append(args, pattern, searchPath)

	cmd := exec.CommandContext(ctx, rgPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to start ripgrep: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to start ripgrep: %w", err)
	}

	type match struct {
		filePath   string
		lineNumber int
	}

	var matches []match
	matchCount := 0
	matchLimitReached := false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		if matchCount >= limit {
			matchLimitReached = true
			break
		}

		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.Type == "match" {
			matchCount++
			if event.Data.Path.Text != "" && event.Data.LineNumber > 0 {
				matches = append(matches, match{
					filePath:   event.Data.Path.Text,
					lineNumber: event.Data.LineNumber,
				})
			}
		}
	}

	// Kill if we hit the limit
	if matchLimitReached {
		cmd.Process.Kill()
	}
	cmd.Wait() // Ignore exit error (rg returns 1 for no matches)

	if matchCount == 0 {
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{
				{Type: "text", Text: "No matches found"},
			},
		}, nil
	}

	// Format output
	formatPath := func(filePath string) string {
		if isDirectory {
			rel, err := filepath.Rel(searchPath, filePath)
			if err == nil && !strings.HasPrefix(rel, "..") {
				return strings.ReplaceAll(rel, "\\", "/")
			}
		}
		return filepath.Base(filePath)
	}

	// File cache for context lines
	fileCache := make(map[string][]string)
	getFileLines := func(filePath string) []string {
		if lines, ok := fileCache[filePath]; ok {
			return lines
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			fileCache[filePath] = nil
			return nil
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		lines := strings.Split(content, "\n")
		fileCache[filePath] = lines
		return lines
	}

	var outputLines []string
	linesTruncated := false

	for _, m := range matches {
		relativePath := formatPath(m.filePath)
		lines := getFileLines(m.filePath)
		if lines == nil {
			outputLines = append(outputLines, fmt.Sprintf("%s:%d: (unable to read file)", relativePath, m.lineNumber))
			continue
		}

		start := m.lineNumber
		end := m.lineNumber
		if contextLines > 0 {
			start = max(1, m.lineNumber-contextLines)
			end = min(len(lines), m.lineNumber+contextLines)
		}

		for current := start; current <= end; current++ {
			lineText := ""
			if current-1 < len(lines) {
				lineText = strings.ReplaceAll(lines[current-1], "\r", "")
			}

			truncatedText, wasTruncated := TruncateLine(lineText, GrepMaxLineLength)
			if wasTruncated {
				linesTruncated = true
			}

			if current == m.lineNumber {
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", relativePath, current, truncatedText))
			} else {
				outputLines = append(outputLines, fmt.Sprintf("%s-%d- %s", relativePath, current, truncatedText))
			}
		}
	}

	rawOutput := strings.Join(outputLines, "\n")
	truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: math.MaxInt})

	output := truncation.Content
	var details *GrepToolDetails

	var notices []string
	if matchLimitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", GrepMaxLineLength))
	}

	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}

	if matchLimitReached || truncation.Truncated || linesTruncated {
		details = &GrepToolDetails{LinesTruncated: linesTruncated}
		if matchLimitReached {
			details.MatchLimitReached = &limit
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
}

// grepFallback uses the system grep command when ripgrep is not available.
func grepFallback(ctx context.Context, pattern, searchPath string, isDirectory, ignoreCase, literal bool, glob string, limit int) (agent.AgentToolResult, error) {
	args := []string{"-r", "-n", "--color=never"}

	if ignoreCase {
		args = append(args, "-i")
	}
	if literal {
		args = append(args, "-F")
	}
	if glob != "" {
		args = append(args, "--include="+glob)
	}

	args = append(args, pattern, searchPath)

	cmd := exec.CommandContext(ctx, "grep", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: "text", Text: "No matches found"},
				},
			}, nil
		}
		return agent.AgentToolResult{}, fmt.Errorf("grep failed: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{
				{Type: "text", Text: "No matches found"},
			},
		}, nil
	}

	// Apply limit
	lines := strings.Split(output, "\n")
	matchLimitReached := len(lines) > limit
	if matchLimitReached {
		lines = lines[:limit]
	}

	// Relativize paths
	for i, line := range lines {
		if strings.HasPrefix(line, searchPath) {
			line = line[len(searchPath):]
			line = strings.TrimPrefix(line, "/")
			lines[i] = line
		}
	}

	rawOutput := strings.Join(lines, "\n")
	truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: math.MaxInt})

	resultOutput := truncation.Content
	var notices []string
	if matchLimitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached", limit))
	}
	if truncation.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
	}
	if len(notices) > 0 {
		resultOutput += "\n\n[" + strings.Join(notices, ". ") + "]"
	}

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: resultOutput},
		},
	}, nil
}


