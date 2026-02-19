// Ported from: packages/coding-agent/src/core/tools/read.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
)

// ReadToolParams are the parameters for the read tool.
type ReadToolParams struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"` // 1-indexed line number
	Limit  *int   `json:"limit,omitempty"`
}

// ReadToolDetails contains details about truncation that occurred.
type ReadToolDetails struct {
	Truncation *TruncationResult `json:"truncation,omitempty"`
}

// SupportedImageExtensions lists file extensions treated as images.
var SupportedImageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// NewReadTool creates the read tool for the given working directory.
func NewReadTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "read",
			Description: fmt.Sprintf(
				"Read the contents of a file. Supports text files and images (jpg, png, gif, webp). "+
					"Images are sent as attachments. For text files, output is truncated to %d lines or %dKB "+
					"(whichever is hit first). Use offset/limit for large files. When you need the full file, "+
					"continue with offset until complete.",
				DefaultMaxLines, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to read (relative or absolute)",
					},
					"offset": map[string]any{
						"type":        "number",
						"description": "Line number to start reading from (1-indexed)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum number of lines to read",
					},
				},
				"required": []string{"path"},
			},
		},
		Label: "read",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			path, _ := params["path"].(string)
			if path == "" {
				return agent.AgentToolResult{}, fmt.Errorf("path is required")
			}

			var offset *int
			if v, ok := params["offset"].(float64); ok {
				i := int(v)
				offset = &i
			}
			var limit *int
			if v, ok := params["limit"].(float64); ok {
				i := int(v)
				limit = &i
			}

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			return executeRead(path, cwd, offset, limit)
		},
	}
}

// executeRead reads a file and returns the result.
func executeRead(path, cwd string, offset, limit *int) (agent.AgentToolResult, error) {
	absolutePath := ResolveReadPath(path, cwd)

	// Check if file exists and is readable
	info, err := os.Stat(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("file not found: %s", path)
	}
	if info.IsDir() {
		return agent.AgentToolResult{}, fmt.Errorf("%s is a directory, not a file", path)
	}

	// Check if it's an image by extension
	ext := strings.ToLower(filepath.Ext(absolutePath))
	if mimeType, ok := SupportedImageExtensions[ext]; ok {
		return readImage(absolutePath, path, mimeType)
	}

	// Read as text
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return applyReadFilters(path, string(data), offset, limit)
}

// readImage reads an image file, resizes if needed, and returns it as base64.
func readImage(absolutePath, displayPath, mimeType string) (agent.AgentToolResult, error) {
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read image %s: %w", displayPath, err)
	}

	b64 := base64.StdEncoding.EncodeToString(data)

	// Resize image if needed (max 2000x2000, max 4.5MB)
	resized := ResizeImage(b64, mimeType, nil)
	textNote := fmt.Sprintf("Read image file [%s]", resized.MimeType)
	if dimNote := FormatDimensionNote(resized); dimNote != "" {
		textNote += "\n" + dimNote
	}

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: textNote},
			{Type: "image", Data: resized.Data, MimeType: resized.MimeType},
		},
	}, nil
}

// ReadFileFn is a function that reads a file and returns its text content.
// Used for ACP client delegation.
type ReadFileFn func(ctx context.Context, path string) (string, error)

// applyReadFilters applies offset, limit, and truncation to already-loaded text content.
// This is the text-processing core of executeRead, extracted for reuse.
func applyReadFilters(path, textContent string, offset, limit *int) (agent.AgentToolResult, error) {
	allLines := strings.Split(textContent, "\n")
	totalFileLines := len(allLines)

	startLine := 0
	if offset != nil {
		startLine = *offset - 1
		if startLine < 0 {
			startLine = 0
		}
	}
	startLineDisplay := startLine + 1

	if startLine >= len(allLines) {
		return agent.AgentToolResult{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", *offset, len(allLines))
	}

	var selectedContent string
	var userLimitedLines *int
	if limit != nil {
		endLine := startLine + *limit
		if endLine > len(allLines) {
			endLine = len(allLines)
		}
		selectedContent = strings.Join(allLines[startLine:endLine], "\n")
		n := endLine - startLine
		userLimitedLines = &n
	} else {
		selectedContent = strings.Join(allLines[startLine:], "\n")
	}

	truncation := TruncateHead(selectedContent, TruncationOptions{})

	var outputText string
	var details *ReadToolDetails

	if truncation.FirstLineExceedsLimit {
		firstLineSize := FormatSize(len(allLines[startLine]))
		outputText = fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, firstLineSize, FormatSize(DefaultMaxBytes), startLineDisplay, path, DefaultMaxBytes)
		details = &ReadToolDetails{Truncation: &truncation}
	} else if truncation.Truncated {
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		nextOffset := endLineDisplay + 1
		outputText = truncation.Content
		if truncation.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, FormatSize(DefaultMaxBytes), nextOffset)
		}
		details = &ReadToolDetails{Truncation: &truncation}
	} else if userLimitedLines != nil && startLine+*userLimitedLines < len(allLines) {
		remaining := len(allLines) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		outputText = truncation.Content
		outputText += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", remaining, nextOffset)
	} else {
		outputText = truncation.Content
	}

	result := agent.AgentToolResult{
		Content: []ai.ToolResultContent{{Type: "text", Text: outputText}},
	}
	if details != nil {
		result.Details = details
	}
	return result, nil
}

// NewReadToolWithReader creates a read tool that delegates text file reads to readFn.
// Image files are still read locally (ACP clients don't expose binary file reading).
func NewReadToolWithReader(cwd string, readFn ReadFileFn) agent.AgentTool {
	t := NewReadTool(cwd)
	orig := t.Execute
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		path, _ := params["path"].(string)
		if path == "" {
			return agent.AgentToolResult{}, fmt.Errorf("path is required")
		}
		absolutePath := ResolveReadPath(path, cwd)
		// Delegate images to original (ACP has no binary read).
		ext := strings.ToLower(filepath.Ext(absolutePath))
		if _, isImage := SupportedImageExtensions[ext]; isImage {
			return orig(ctx, toolCallID, params, onUpdate)
		}
		// Delegate text reads to the provided function.
		content, err := readFn(ctx, absolutePath)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("failed to read %s: %w", path, err)
		}
		var offset *int
		if v, ok := params["offset"].(float64); ok {
			i := int(v)
			offset = &i
		}
		var limit *int
		if v, ok := params["limit"].(float64); ok {
			i := int(v)
			limit = &i
		}
		return applyReadFilters(path, content, offset, limit)
	}
	return t
}
