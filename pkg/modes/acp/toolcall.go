// ACP tool call mapping, display, and content building.
// Split from helpers.go.
package acp

import (
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
)

// MapToolKind maps a builtin tool name to an ACP ToolKind. Non-builtin names
// return "other"; use MapToolKindForCall to also apply the args/hint heuristic.
func MapToolKind(toolName string) acpsdk.ToolKind {
	switch toolName {
	case "read":
		return "read"
	case "write", "edit":
		return "edit"
	case "bash", "bash_output", "bash_kill":
		return "execute"
	case "grep", "find":
		return "search"
	default:
		return "other"
	}
}

// MapToolKindForCall maps a tool call to an ACP ToolKind. Builtin names keep
// their fixed mapping; for everything else it applies a conservative,
// name-agnostic heuristic driven off the call's argument names (and the
// display hint's TitleArgs) so extension tools still get a meaningful
// client-side icon instead of the generic "other".
func MapToolKindForCall(toolName string, args map[string]interface{}, hint *agent.ToolDisplayHint) acpsdk.ToolKind {
	if k := MapToolKind(toolName); k != "other" {
		return k
	}
	has := func(key string) bool {
		if _, ok := args[key]; ok {
			return true
		}
		if hint != nil {
			for _, ta := range hint.TitleArgs {
				if ta.Name == key {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("command"):
		return "execute"
	case has("pattern"):
		return "search"
	case has("newText") || has("oldText") || has("content"):
		return "edit"
	case has("path"):
		return "read"
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
	return BuildToolInitialContentWithHint(toolName, args, nil)
}

// BuildToolInitialContentWithHint is BuildToolInitialContent, but for hinted
// extension tools it builds a start-content block from the display hint —
// mirroring how bash gets a "$ <cmd>" preview. When the hint requests a box
// (use_box) and the call carries a command-like arg, the command is shown in a
// fenced block, preceded by any accent context args (e.g. rexec's host).
func BuildToolInitialContentWithHint(toolName string, args map[string]interface{}, hint *agent.ToolDisplayHint) []acpsdk.ToolCallContent {
	if hint != nil && hint.UseBox {
		if c := hintInitialContent(args, hint); c != nil {
			return c
		}
	}
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
	return BuildToolTitleWithHint(toolName, args, nil)
}

// BuildToolTitleWithHint returns a human-readable title for a tool call. When
// a display hint with TitleArgs is present (extension tools), the title is
// built from those args — mirroring the TUI's formatWithHint semantics (arg
// order, boolean args rendered as badges, missing/empty args skipped). Without
// such a hint it falls back to the builtin switch, so builtin titles stay
// byte-identical.
func BuildToolTitleWithHint(toolName string, args map[string]interface{}, hint *agent.ToolDisplayHint) string {
	if hint != nil && len(hint.TitleArgs) > 0 {
		return buildHintedTitle(toolName, args, hint)
	}
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

// buildHintedTitle builds a tool title from a display hint's TitleArgs,
// mirroring the interactive TUI's formatWithHint semantics but rendered as
// plain markdown-safe text (ACP carries no theme colours). Order follows the
// hint; boolean args become badges (label, or name, shown only when true);
// missing or empty args are skipped; "pattern" style values are wrapped in
// /…/. The joined result reuses the bash 80-char + ellipsis truncation and
// escapes backticks so a stray backtick can't break client-side markdown.
func buildHintedTitle(toolName string, args map[string]interface{}, hint *agent.ToolDisplayHint) string {
	parts := []string{toolName}
	for _, ta := range hint.TitleArgs {
		// Boolean args act as badges: render the label (or name) only when the
		// flag is true, dropping the "true"/"false" value text entirely.
		if raw, exists := args[ta.Name]; exists {
			if b, isBool := raw.(bool); isBool {
				if b {
					badge := ta.Label
					if badge == "" {
						badge = ta.Name
					}
					parts = append(parts, badge)
				}
				continue
			}
		}

		val, valid := strArgChecked(args, ta.Name)
		if !valid {
			// Present but not a string — format as a generic value.
			if raw, exists := args[ta.Name]; exists && raw != nil {
				val = fmt.Sprintf("%v", raw)
			} else {
				continue
			}
		}
		if val == "" {
			continue
		}
		if ta.Style == "pattern" {
			val = "/" + val + "/"
		}
		if ta.Label != "" {
			val = ta.Label + " " + val
		}
		parts = append(parts, val)
	}

	title := strings.Join(parts, " ")
	const maxLen = 80
	if len(title) > maxLen {
		title = title[:maxLen] + "..."
	}
	return strings.ReplaceAll(title, "`", "\\`")
}

// strArgChecked returns (value, valid) where valid=false means the arg exists
// but is not a string (invalid type). Missing or nil is (empty, true). Mirrors
// the interactive component helper of the same name.
func strArgChecked(args map[string]interface{}, key string) (string, bool) {
	if args == nil {
		return "", true
	}
	v, exists := args[key]
	if !exists || v == nil {
		return "", true
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// hintInitialContent builds a start-content block for a hinted, boxed tool
// from its TitleArgs: string context args are shown as "label: value" lines and
// a "command" arg is shown as a fenced "$ <cmd>" block. Returns nil when there
// is nothing useful to show, so the caller can fall back.
func hintInitialContent(args map[string]interface{}, hint *agent.ToolDisplayHint) []acpsdk.ToolCallContent {
	var context []string
	var command string
	for _, ta := range hint.TitleArgs {
		val, valid := strArgChecked(args, ta.Name)
		if !valid || val == "" {
			continue
		}
		if ta.Name == "command" {
			command = val
			continue
		}
		label := ta.Label
		if label == "" {
			label = ta.Name
		}
		context = append(context, label+": "+val)
	}
	if command == "" && len(context) == 0 {
		return nil
	}
	var b strings.Builder
	for _, line := range context {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if command != "" {
		b.WriteString("```\n$ ")
		b.WriteString(command)
		b.WriteString("\n```")
	} else {
		// Drop the trailing newline when there is no command block.
		return []acpsdk.ToolCallContent{textContent(strings.TrimRight(b.String(), "\n"))}
	}
	return []acpsdk.ToolCallContent{textContent(b.String())}
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

	case "read", "bash", "bash_output", "bash_kill", "grep", "find":
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
