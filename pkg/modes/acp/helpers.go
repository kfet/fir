// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts (helper functions)
// Upstream hash: pi-mono-acp branch
package acp

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ParseModelID splits an ACP model ID "provider/modelId" into its components.
func ParseModelID(acpModelID string) (provider, modelID string, err error) {
	idx := strings.Index(acpModelID, "/")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid model ID format: %s. Expected \"provider/modelId\"", acpModelID)
	}
	return acpModelID[:idx], acpModelID[idx+1:], nil
}

// shortProvider abbreviates provider names for display.
func shortProvider(provider string) string {
	m := map[string]string{
		"anthropic": "anth", "openai": "oai", "google": "goog",
		"mistral": "mist", "groq": "groq", "openrouter": "or",
		"bedrock": "bed", "vertex": "vtx", "azure": "az",
		"deepseek": "ds", "xai": "xai",
	}
	if s, ok := m[provider]; ok {
		return s
	}
	return provider
}

// BuildModelState creates an ACP SessionModelState from the model registry.
// Only includes models that have auth configured (API key or OAuth token).
func BuildModelState(reg *models.ModelRegistry, currentModel *ai.Model) *acpsdk.SessionModelState {
	if currentModel == nil {
		return nil
	}
	available := reg.GetAvailable()
	models := make([]acpsdk.ModelInfo, 0, len(available))
	for _, m := range available {
		models = append(models, acpsdk.ModelInfo{
			ModelId: acpsdk.ModelId(fmt.Sprintf("%s/%s", m.Provider, m.ID)),
			Name:    fmt.Sprintf("%s / %s", m.Name, shortProvider(m.Provider)),
		})
	}
	return &acpsdk.SessionModelState{
		AvailableModels: models,
		CurrentModelId:  acpsdk.ModelId(fmt.Sprintf("%s/%s", currentModel.Provider, currentModel.ID)),
	}
}

// ExtractPromptContent extracts text and images from ACP content blocks.
func ExtractPromptContent(blocks []acpsdk.ContentBlock) (text string, images []ai.ImageContent) {
	var textParts []string
	for _, block := range blocks {
		if block.Text != nil {
			textParts = append(textParts, block.Text.Text)
		} else if block.Image != nil {
			images = append(images, ai.ImageContent{
				Type:     "image",
				MimeType: block.Image.MimeType,
				Data:     block.Image.Data,
			})
		} else if block.Resource != nil && block.Resource.Resource.TextResourceContents != nil {
			textParts = append(textParts, block.Resource.Resource.TextResourceContents.Text)
		} else if block.ResourceLink != nil && strings.HasPrefix(block.ResourceLink.Uri, "file://") {
			textParts = append(textParts, "@"+block.ResourceLink.Uri[7:])
		}
	}
	return strings.Join(textParts, "\n"), images
}

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

// diffLineRegexp matches pi's diff format: prefix + line number + space + content.
var diffLineRegexp = regexp.MustCompile(`^([-+ ])(\s*\d+)\s(.*)$`)
var codeFenceRegexp = regexp.MustCompile("(?m)^`{3,}")

// ParseDiffForAcp parses pi's custom diff format and extracts content and locations for ACP.
func ParseDiffForAcp(diffText, filePath string, firstChangedLine int) (content []acpsdk.ToolCallContent, locations []acpsdk.ToolCallLocation) {
	if diffText == "" || filePath == "" {
		return nil, nil
	}

	lines := strings.Split(diffText, "\n")
	var oldLines, newLines []string

	for _, line := range lines {
		if strings.TrimSpace(line) == "..." {
			continue
		}
		match := diffLineRegexp.FindStringSubmatch(line)
		if match != nil {
			prefix := match[1]
			lineContent := match[3]
			switch prefix {
			case "-":
				oldLines = append(oldLines, lineContent)
			case "+":
				newLines = append(newLines, lineContent)
			default: // space = context
				oldLines = append(oldLines, lineContent)
				newLines = append(newLines, lineContent)
			}
		}
	}

	if len(oldLines) > 0 || len(newLines) > 0 {
		line := firstChangedLine
		if line == 0 {
			line = 1
		}
		locations = append(locations, acpsdk.ToolCallLocation{Path: filePath, Line: &line})
		var oldPtr *string
		if len(oldLines) > 0 {
			s := strings.Join(oldLines, "\n")
			oldPtr = &s
		}
		newText := strings.Join(newLines, "\n")
		content = append(content, diffContent(filePath, oldPtr, newText))
	}

	return content, locations
}

// MarkdownEscape wraps text in code fences, using enough backticks to avoid conflicts.
func MarkdownEscape(text string) string {
	fence := "```"
	for _, match := range codeFenceRegexp.FindAllString(text, -1) {
		for len(match) >= len(fence) {
			fence += "`"
		}
	}
	suffix := ""
	if !strings.HasSuffix(text, "\n") {
		suffix = "\n"
	}
	return fence + "\n" + text + suffix + fence
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

// IsPathWithinDirectory checks that targetPath is within baseDirectory.
func IsPathWithinDirectory(targetPath, baseDirectory string) bool {
	normalizedTarget := filepath.Clean(targetPath)
	normalizedBase := filepath.Clean(baseDirectory)
	rel, err := filepath.Rel(normalizedBase, normalizedTarget)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
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

// extractResultText extracts text from a tool result's content array.
func extractResultText(result interface{}) string {
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return ""
	}
	contentArr, ok := resultMap["content"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range contentArr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "text" {
			if text, ok := m["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
