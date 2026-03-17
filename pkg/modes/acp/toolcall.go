// ACP tool call mapping, display, and content building.
// Split from helpers.go.
package acp

import (
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// MapToolKind maps a tool name to an ACP ToolKind.
func MapToolKind(toolName string) acpsdk.ToolKind {
	switch toolName {
	case "read":
		return "read"
	case "write", "edit":
		return "edit"
	case "bash", "bash_output", "bash_kill":
		return "execute"
	case "grep", "find", "ls":
		return "search"
	default:
		return "other"
	}
}

// BuildToolLocations returns file locations for tools that operate on files.
func BuildToolLocations(toolName string, args map[string]interface{}) []acpsdk.ToolCallLocation {
	filePath, _ := args["path"].(string)
	if filePath == "" {
		return nil
	}
	switch toolName {
	case "read":
		if offset, ok := args["offset"].(float64); ok {
			return []acpsdk.ToolCallLocation{{Path: filePath, Line: intPtr(int(offset))}}
		}
		return []acpsdk.ToolCallLocation{{Path: filePath}}
	case "write", "edit":
		return []acpsdk.ToolCallLocation{{Path: filePath}}
	default:
		return nil
	}
}

// BuildToolInitialContent returns content to display when a tool call starts.
func BuildToolInitialContent(toolName string, args map[string]interface{}) []acpsdk.ToolCallContent {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return []acpsdk.ToolCallContent{textContent("$ " + cmd)}
		}
	case "read":
		if p, ok := args["path"].(string); ok {
			details := []string{"Reading " + p}
			if off, ok := args["offset"].(float64); ok {
				details = append(details, fmt.Sprintf("Starting at line %d", int(off)))
			}
			if lim, ok := args["limit"].(float64); ok {
				details = append(details, fmt.Sprintf("Reading %d lines", int(lim)))
			}
			return []acpsdk.ToolCallContent{textContent(strings.Join(details, "\n"))}
		}
	case "write":
		p, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if p != "" && content != "" {
			return []acpsdk.ToolCallContent{diffContent(p, nil, content)}
		}
	case "edit":
		p, _ := args["path"].(string)
		oldText, _ := args["oldText"].(string)
		newText, _ := args["newText"].(string)
		if p != "" && oldText != "" && newText != "" {
			return []acpsdk.ToolCallContent{diffContent(p, &oldText, newText)}
		}
	}
	return nil
}

// BuildToolTitle returns a human-readable title for a tool call.
func BuildToolTitle(toolName string, args map[string]interface{}) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			const maxLen = 80
			truncated := cmd
			if len(truncated) > maxLen {
				truncated = truncated[:maxLen] + "..."
			}
			escaped := strings.ReplaceAll(truncated, "`", "\\`")
			return "`" + escaped + "`"
		}
		return "Terminal"
	case "read":
		if p, ok := args["path"].(string); ok {
			parts := []string{"Read " + p}
			if off, ok := args["offset"].(float64); ok {
				parts = append(parts, fmt.Sprintf("from line %d", int(off)))
			}
			if lim, ok := args["limit"].(float64); ok {
				parts = append(parts, fmt.Sprintf("(%d lines)", int(lim)))
			}
			return strings.Join(parts, " ")
		}
		return "Read"
	case "write":
		if p, ok := args["path"].(string); ok {
			return "Write " + p
		}
		return "Write"
	case "edit":
		if p, ok := args["path"].(string); ok {
			return "Edit " + p
		}
		return "Edit"
	case "grep":
		pattern, _ := args["pattern"].(string)
		p, _ := args["path"].(string)
		if pattern != "" {
			target := ""
			if p != "" {
				target = " in " + p
			}
			return fmt.Sprintf(`grep "%s"%s`, pattern, target)
		}
		return "Grep"
	case "find":
		pattern, _ := args["pattern"].(string)
		if pattern == "" {
			pattern, _ = args["name"].(string)
		}
		dir, _ := args["path"].(string)
		if dir == "" {
			dir, _ = args["directory"].(string)
		}
		if pattern != "" {
			target := ""
			if dir != "" {
				target = " in " + dir
			}
			return fmt.Sprintf(`find "%s"%s`, pattern, target)
		}
		return "Find"
	case "ls":
		dir, _ := args["path"].(string)
		if dir == "" {
			dir, _ = args["directory"].(string)
		}
		if dir != "" {
			return "ls " + dir
		}
		return "List files"
	case "bash_output":
		if cmdID, ok := args["command_id"].(string); ok {
			if len(cmdID) > 40 {
				cmdID = cmdID[:40] + "..."
			}
			return "Get output: " + cmdID
		}
		return "Get background command output"
	case "bash_kill":
		if cmdID, ok := args["command_id"].(string); ok {
			if len(cmdID) > 40 {
				cmdID = cmdID[:40] + "..."
			}
			return "Kill: " + cmdID
		}
		return "Kill background command"
	default:
		return toolName
	}
}

// BuildToolCallContent builds the content and locations for a completed tool call.
func BuildToolCallContent(toolName string, args map[string]interface{}, result interface{}, isError bool) (content []acpsdk.ToolCallContent, locations []acpsdk.ToolCallLocation) {
	if isError {
		errorText := extractResultText(result)
		if errorText == "" {
			errorText = fmt.Sprint(result)
		}
		return []acpsdk.ToolCallContent{textContent(MarkdownEscape(errorText))}, nil
	}

	switch toolName {
	case "edit":
		filePath, _ := args["path"].(string)
		resultMap, _ := result.(map[string]interface{})
		if resultMap != nil {
			details, _ := resultMap["details"].(map[string]interface{})
			if details != nil {
				diffText, _ := details["diff"].(string)
				firstLine, _ := details["firstChangedLine"].(float64)
				if diffText != "" && filePath != "" {
					c, l := ParseDiffForAcp(diffText, filePath, int(firstLine))
					if len(c) > 0 {
						return c, l
					}
				}
			}
		}
		return nil, nil

	case "write":
		return nil, nil

	case "read", "bash", "bash_output", "bash_kill", "grep", "find", "ls":
		text := extractResultText(result)
		if text != "" {
			return []acpsdk.ToolCallContent{textContent(MarkdownEscape(text))}, nil
		}
		return nil, nil

	default:
		text := extractResultText(result)
		if text != "" {
			return []acpsdk.ToolCallContent{textContent(text)}, nil
		}
		return nil, nil
	}
}

// ============================================================================
// Internal helpers
// ============================================================================

func intPtr(n int) *int {
	return &n
}

// textContent creates a ToolCallContent with text.
func textContent(text string) acpsdk.ToolCallContent {
	return acpsdk.ToolCallContent{
		Content: &acpsdk.ToolCallContentContent{
			Type: "content",
			Content: acpsdk.ContentBlock{
				Text: &acpsdk.ContentBlockText{Type: "text", Text: text},
			},
		},
	}
}

// diffContent creates a ToolCallContent with a diff.
func diffContent(path string, oldText *string, newText string) acpsdk.ToolCallContent {
	return acpsdk.ToolCallContent{
		Diff: &acpsdk.ToolCallContentDiff{
			Type:    "diff",
			Path:    path,
			OldText: oldText,
			NewText: newText,
		},
	}
}

// terminalContent creates a ToolCallContent for an embedded terminal.
func terminalContent(terminalID string) acpsdk.ToolCallContent {
	return acpsdk.ToolCallContent{
		Terminal: &acpsdk.ToolCallContentTerminal{
			Type:       "terminal",
			TerminalId: terminalID,
		},
	}
}
