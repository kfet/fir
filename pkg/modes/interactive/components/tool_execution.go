// Ported from: packages/coding-agent/src/modes/interactive/components/tool-execution.ts
// Upstream hash: 3b1f8e5d
package components

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

const bashPreviewLines = 5

// ToolExecutionOptions configures tool execution display.
type ToolExecutionOptions struct {
	ShowImages bool // default: true
}

// ToolResultData holds tool execution result content.
type ToolResultData struct {
	Content []ToolContentBlock
	IsError bool
	Details map[string]any
}

// ToolContentBlock represents a content block in a tool result.
type ToolContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolExecutionComponent renders a tool call with its result.
type ToolExecutionComponent struct {
	tui.Container
	contentText *tuicomp.Text
	contentBox  *tuicomp.Box
	toolName    string
	args        map[string]any
	expanded    bool
	showImages  bool
	isPartial   bool
	useBox      bool
	result      *ToolResultData
}

// NewToolExecutionComponent creates a new ToolExecutionComponent.
func NewToolExecutionComponent(
	toolName string,
	args map[string]any,
	opts *ToolExecutionOptions,
) *ToolExecutionComponent {
	t := theme.GetTheme()
	showImages := true
	if opts != nil {
		showImages = opts.ShowImages
	}

	useBox := toolName == "bash"
	tc := &ToolExecutionComponent{
		toolName:   toolName,
		args:       args,
		showImages: showImages,
		isPartial:  true,
		useBox:     useBox,
	}

	tc.AddChild(tuicomp.NewSpacer(1))

	bgFn := func(s string) string { return t.Bg("toolPendingBg", s) }
	if useBox {
		tc.contentBox = tuicomp.NewBox(1, 1, bgFn)
		tc.AddChild(tc.contentBox)
	} else {
		tc.contentText = tuicomp.NewText("", 1, 1, bgFn)
		tc.AddChild(tc.contentText)
	}

	tc.updateDisplay()
	return tc
}

// UpdateArgs updates the tool arguments and refreshes.
func (tc *ToolExecutionComponent) UpdateArgs(args map[string]any) {
	tc.args = args
	tc.updateDisplay()
}

// UpdateResult updates the tool result.
func (tc *ToolExecutionComponent) UpdateResult(result *ToolResultData, isPartial bool) {
	tc.result = result
	tc.isPartial = isPartial
	tc.updateDisplay()
}

// SetExpanded sets the expanded state.
func (tc *ToolExecutionComponent) SetExpanded(expanded bool) {
	tc.expanded = expanded
	tc.updateDisplay()
}

// SetShowImages sets whether images are shown.
func (tc *ToolExecutionComponent) SetShowImages(show bool) {
	tc.showImages = show
	tc.updateDisplay()
}

// Invalidate rebuilds the display.
func (tc *ToolExecutionComponent) Invalidate() {
	tc.Container.Invalidate()
	tc.updateDisplay()
}

func (tc *ToolExecutionComponent) updateDisplay() {
	t := theme.GetTheme()

	bgFn := func(s string) string { return t.Bg("toolPendingBg", s) }
	if !tc.isPartial {
		if tc.result != nil && tc.result.IsError {
			bgFn = func(s string) string { return t.Bg("toolErrorBg", s) }
		} else {
			bgFn = func(s string) string { return t.Bg("toolSuccessBg", s) }
		}
	}

	if tc.useBox {
		tc.contentBox.SetBgFn(bgFn)
		tc.contentBox.Clear()
		tc.renderBashContent()
	} else {
		tc.contentText.SetCustomBgFn(bgFn)
		tc.contentText.SetText(tc.formatToolExecution())
	}
}

func (tc *ToolExecutionComponent) renderBashContent() {
	t := theme.GetTheme()
	command := strArg(tc.args, "command")

	commandDisplay := command
	if command == "" {
		commandDisplay = t.Fg("toolOutput", "...")
	}

	timeout, _ := tc.args["timeout"].(float64)
	timeoutSuffix := ""
	if timeout > 0 {
		timeoutSuffix = t.Fg("muted", fmt.Sprintf(" (timeout %.0fs)", timeout))
	}

	tc.contentBox.AddChild(tuicomp.NewText(
		t.Fg("toolTitle", t.Bold("$ "+commandDisplay))+timeoutSuffix, 0, 0, nil))

	if tc.result != nil {
		output := strings.TrimSpace(tc.getTextOutput())
		if output != "" {
			styledLines := make([]string, 0)
			for _, line := range strings.Split(output, "\n") {
				styledLines = append(styledLines, t.Fg("toolOutput", line))
			}
			styledOutput := strings.Join(styledLines, "\n")

			if tc.expanded {
				tc.contentBox.AddChild(tuicomp.NewText("\n"+styledOutput, 0, 0, nil))
			} else {
				// Use visual line truncation when collapsed
				tc.contentBox.AddChild(&bashTruncatedOutput{
					styledOutput: styledOutput,
					maxLines:     bashPreviewLines,
				})
			}
		}

		// Truncation warnings
		tc.addTruncationWarnings()
	}
}

// bashTruncatedOutput is a component that truncates bash output to visual lines.
type bashTruncatedOutput struct {
	styledOutput string
	maxLines     int
}

func (b *bashTruncatedOutput) Invalidate() {}

func (b *bashTruncatedOutput) Render(width int) []string {
	t := theme.GetTheme()
	result := TruncateToVisualLines(b.styledOutput, b.maxLines, width, 0)
	if result.SkippedCount > 0 {
		hint := t.Fg("muted", fmt.Sprintf("... (%d earlier lines,", result.SkippedCount)) +
			" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
		lines := []string{"", tui.TruncateToWidth(hint, width, "...", false)}
		return append(lines, result.VisualLines...)
	}
	return append([]string{""}, result.VisualLines...)
}

func (tc *ToolExecutionComponent) addTruncationWarnings() {
	if tc.result == nil || tc.result.Details == nil {
		return
	}
	t := theme.GetTheme()

	var warnings []string
	if fp, ok := tc.result.Details["fullOutputPath"].(string); ok && fp != "" {
		warnings = append(warnings, "Full output: "+fp)
	}
	if trunc, ok := tc.result.Details["truncation"].(map[string]any); ok {
		if isTruncated(trunc) {
			if truncBy, _ := trunc["truncatedBy"].(string); truncBy == "lines" {
				outLines := intFromAny(trunc["outputLines"])
				totalLines := intFromAny(trunc["totalLines"])
				warnings = append(warnings, fmt.Sprintf("Truncated: showing %d of %d lines", outLines, totalLines))
			} else {
				outLines := intFromAny(trunc["outputLines"])
				maxBytes := intFromAny(trunc["maxBytes"])
				if maxBytes == 0 {
					maxBytes = 50 * 1024
				}
				warnings = append(warnings, fmt.Sprintf("Truncated: %d lines shown (%s limit)", outLines, formatSize(maxBytes)))
			}
		}
	}
	if len(warnings) > 0 {
		tc.contentBox.AddChild(tuicomp.NewText("\n"+t.Fg("warning", "["+strings.Join(warnings, ". ")+"]"), 0, 0, nil))
	}
}

func (tc *ToolExecutionComponent) getTextOutput() string {
	if tc.result == nil {
		return ""
	}
	var parts []string
	for _, c := range tc.result.Content {
		if c.Type == "text" {
			parts = append(parts, strings.ReplaceAll(c.Text, "\r", ""))
		}
	}
	return strings.Join(parts, "\n")
}

func (tc *ToolExecutionComponent) formatToolExecution() string {
	t := theme.GetTheme()
	invalidArg := t.Fg("error", "[invalid arg]")

	switch tc.toolName {
	case "read":
		return tc.formatRead(t, invalidArg)
	case "write":
		return tc.formatWrite(t, invalidArg)
	case "edit":
		return tc.formatEdit(t, invalidArg)
	case "ls":
		return tc.formatLs(t, invalidArg)
	case "find":
		return tc.formatFind(t, invalidArg)
	case "grep":
		return tc.formatGrep(t, invalidArg)
	default:
		return tc.formatGeneric(t)
	}
}

func (tc *ToolExecutionComponent) formatRead(t *theme.Theme, invalidArg string) string {
	path := strArgAlt(tc.args, "file_path", "path")
	offset, _ := tc.args["offset"].(float64)
	limit, _ := tc.args["limit"].(float64)

	var pathDisplay string
	if path == "" {
		pathDisplay = t.Fg("toolOutput", "...")
	} else {
		pathDisplay = t.Fg("accent", shortenPath(path))
	}
	if offset > 0 || limit > 0 {
		startLine := int(offset)
		if startLine == 0 {
			startLine = 1
		}
		if limit > 0 {
			pathDisplay += t.Fg("warning", fmt.Sprintf(":%d-%d", startLine, startLine+int(limit)-1))
		} else {
			pathDisplay += t.Fg("warning", fmt.Sprintf(":%d", startLine))
		}
	}

	text := t.Fg("toolTitle", t.Bold("read")) + " " + pathDisplay

	if tc.result != nil {
		output := tc.getTextOutput()
		lines := strings.Split(replaceTabs(output), "\n")
		maxLines := 10
		if tc.expanded {
			maxLines = len(lines)
		}
		displayLines := lines
		if len(displayLines) > maxLines {
			displayLines = lines[:maxLines]
		}
		remaining := len(lines) - len(displayLines)

		styledLines := make([]string, len(displayLines))
		for i, line := range displayLines {
			styledLines[i] = t.Fg("toolOutput", line)
		}
		text += "\n\n" + strings.Join(styledLines, "\n")
		if remaining > 0 {
			text += t.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
				" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
		}
		text += tc.formatReadTruncation(t)
	}
	return text
}

func (tc *ToolExecutionComponent) formatReadTruncation(t *theme.Theme) string {
	if tc.result == nil || tc.result.Details == nil {
		return ""
	}
	trunc, ok := tc.result.Details["truncation"].(map[string]any)
	if !ok || !isTruncated(trunc) {
		return ""
	}
	if firstLine, _ := trunc["firstLineExceedsLimit"].(bool); firstLine {
		maxBytes := intFromAny(trunc["maxBytes"])
		if maxBytes == 0 {
			maxBytes = 50 * 1024
		}
		return "\n" + t.Fg("warning", fmt.Sprintf("[First line exceeds %s limit]", formatSize(maxBytes)))
	}
	if truncBy, _ := trunc["truncatedBy"].(string); truncBy == "lines" {
		outLines := intFromAny(trunc["outputLines"])
		totalLines := intFromAny(trunc["totalLines"])
		maxLines := intFromAny(trunc["maxLines"])
		if maxLines == 0 {
			maxLines = 2000
		}
		return "\n" + t.Fg("warning", fmt.Sprintf("[Truncated: showing %d of %d lines (%d line limit)]", outLines, totalLines, maxLines))
	}
	outLines := intFromAny(trunc["outputLines"])
	maxBytes := intFromAny(trunc["maxBytes"])
	if maxBytes == 0 {
		maxBytes = 50 * 1024
	}
	return "\n" + t.Fg("warning", fmt.Sprintf("[Truncated: %d lines shown (%s limit)]", outLines, formatSize(maxBytes)))
}

func (tc *ToolExecutionComponent) formatWrite(t *theme.Theme, _ string) string {
	path := strArgAlt(tc.args, "file_path", "path")
	content := strArg(tc.args, "content")

	var pathDisplay string
	if path == "" {
		pathDisplay = t.Fg("toolOutput", "...")
	} else {
		pathDisplay = t.Fg("accent", shortenPath(path))
	}
	text := t.Fg("toolTitle", t.Bold("write")) + " " + pathDisplay

	if content != "" {
		lines := strings.Split(replaceTabs(content), "\n")
		maxLines := 10
		if tc.expanded {
			maxLines = len(lines)
		}
		displayLines := lines
		if len(displayLines) > maxLines {
			displayLines = lines[:maxLines]
		}
		remaining := len(lines) - len(displayLines)

		styledLines := make([]string, len(displayLines))
		for i, line := range displayLines {
			styledLines[i] = t.Fg("toolOutput", line)
		}
		text += "\n\n" + strings.Join(styledLines, "\n")
		if remaining > 0 {
			text += t.Fg("muted", fmt.Sprintf("\n... (%d more lines, %d total,", remaining, len(lines))) +
				" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
		}
	}

	if tc.result != nil && tc.result.IsError {
		errorText := tc.getTextOutput()
		if errorText != "" {
			text += "\n\n" + t.Fg("error", errorText)
		}
	}
	return text
}

func (tc *ToolExecutionComponent) formatEdit(t *theme.Theme, _ string) string {
	path := strArgAlt(tc.args, "file_path", "path")

	var pathDisplay string
	if path == "" {
		pathDisplay = t.Fg("toolOutput", "...")
	} else {
		pathDisplay = t.Fg("accent", shortenPath(path))
	}

	// Line number hint from result
	if tc.result != nil && !tc.result.IsError && tc.result.Details != nil {
		if firstLine := intFromAny(tc.result.Details["firstChangedLine"]); firstLine > 0 {
			pathDisplay += t.Fg("warning", fmt.Sprintf(":%d", firstLine))
		}
	}

	text := t.Fg("toolTitle", t.Bold("edit")) + " " + pathDisplay

	if tc.result != nil {
		if tc.result.IsError {
			errorText := tc.getTextOutput()
			if errorText != "" {
				text += "\n\n" + t.Fg("error", errorText)
			}
		} else if diff, ok := tc.result.Details["diff"].(string); ok && diff != "" {
			text += "\n\n" + RenderDiff(diff, nil)
		}
	}
	return text
}

func (tc *ToolExecutionComponent) formatLs(t *theme.Theme, _ string) string {
	path := strArg(tc.args, "path")
	if path == "" {
		path = "."
	}
	limit, _ := tc.args["limit"].(float64)

	text := t.Fg("toolTitle", t.Bold("ls")) + " " + t.Fg("accent", shortenPath(path))
	if limit > 0 {
		text += t.Fg("toolOutput", fmt.Sprintf(" (limit %.0f)", limit))
	}

	if tc.result != nil {
		output := strings.TrimSpace(tc.getTextOutput())
		if output != "" {
			lines := strings.Split(output, "\n")
			maxLines := 20
			if tc.expanded {
				maxLines = len(lines)
			}
			displayLines := lines
			if len(displayLines) > maxLines {
				displayLines = lines[:maxLines]
			}
			remaining := len(lines) - len(displayLines)

			styledLines := make([]string, len(displayLines))
			for i, line := range displayLines {
				styledLines[i] = t.Fg("toolOutput", line)
			}
			text += "\n\n" + strings.Join(styledLines, "\n")
			if remaining > 0 {
				text += t.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
					" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
			}
		}
	}
	return text
}

func (tc *ToolExecutionComponent) formatFind(t *theme.Theme, _ string) string {
	pattern := strArg(tc.args, "pattern")
	path := strArg(tc.args, "path")
	if path == "" {
		path = "."
	}
	limit, _ := tc.args["limit"].(float64)

	text := t.Fg("toolTitle", t.Bold("find")) + " " + t.Fg("accent", pattern) +
		t.Fg("toolOutput", " in "+shortenPath(path))
	if limit > 0 {
		text += t.Fg("toolOutput", fmt.Sprintf(" (limit %.0f)", limit))
	}

	if tc.result != nil {
		output := strings.TrimSpace(tc.getTextOutput())
		if output != "" {
			lines := strings.Split(output, "\n")
			maxLines := 20
			if tc.expanded {
				maxLines = len(lines)
			}
			displayLines := lines
			if len(displayLines) > maxLines {
				displayLines = lines[:maxLines]
			}
			remaining := len(lines) - len(displayLines)

			styledLines := make([]string, len(displayLines))
			for i, line := range displayLines {
				styledLines[i] = t.Fg("toolOutput", line)
			}
			text += "\n\n" + strings.Join(styledLines, "\n")
			if remaining > 0 {
				text += t.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
					" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
			}
		}
	}
	return text
}

func (tc *ToolExecutionComponent) formatGrep(t *theme.Theme, _ string) string {
	pattern := strArg(tc.args, "pattern")
	path := strArg(tc.args, "path")
	if path == "" {
		path = "."
	}
	glob := strArg(tc.args, "glob")
	limit, _ := tc.args["limit"].(float64)
	text := t.Fg("toolTitle", t.Bold("grep")) + " " + t.Fg("accent", "/"+pattern+"/") +
		t.Fg("toolOutput", " in "+shortenPath(path))
	if glob != "" {
		text += t.Fg("toolOutput", " ("+glob+")")
	}
	if limit > 0 {
		text += t.Fg("toolOutput", fmt.Sprintf(" limit %.0f", limit))
	}

	if tc.result != nil {
		output := strings.TrimSpace(tc.getTextOutput())
		if output != "" {
			lines := strings.Split(output, "\n")
			maxLines := 15
			if tc.expanded {
				maxLines = len(lines)
			}
			displayLines := lines
			if len(displayLines) > maxLines {
				displayLines = lines[:maxLines]
			}
			remaining := len(lines) - len(displayLines)

			styledLines := make([]string, len(displayLines))
			for i, line := range displayLines {
				styledLines[i] = t.Fg("toolOutput", line)
			}
			text += "\n\n" + strings.Join(styledLines, "\n")
			if remaining > 0 {
				text += t.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
					" " + KeyHint(tuicomp.ActExpandTools, "to expand") + ")"
			}
		}
	}
	return text
}

func (tc *ToolExecutionComponent) formatGeneric(t *theme.Theme) string {
	text := t.Fg("toolTitle", t.Bold(tc.toolName))
	argsJSON, _ := json.MarshalIndent(tc.args, "", "  ")
	text += "\n\n" + string(argsJSON)
	output := tc.getTextOutput()
	if output != "" {
		text += "\n" + output
	}
	return text
}

// --- helpers ---

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return v
}

func strArgAlt(args map[string]any, key1, key2 string) string {
	v := strArg(args, key1)
	if v == "" {
		v = strArg(args, key2)
	}
	return v
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func isTruncated(trunc map[string]any) bool {
	v, _ := trunc["truncated"].(bool)
	return v
}

func formatSize(bytes int) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
	if bytes >= 1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%dB", bytes)
}
