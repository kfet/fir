// Ported from: packages/coding-agent/src/core/system-prompt.ts
// Upstream hash: 1caadb2e
package core

import (
	"fmt"
	"strings"
	"time"
)

// ToolDescriptions maps built-in tool names to descriptions for the system prompt.
var ToolDescriptions = map[string]string{
	"read":  "Read file contents",
	"bash":  "Execute bash commands (ls, grep, find, etc.)",
	"edit":  "Make surgical edits to files (find exact text and replace)",
	"write": "Create or overwrite files",
	"grep":  "Search file contents for patterns (respects .gitignore)",
	"find":  "Find files by glob pattern (respects .gitignore)",
	"ls":    "List directory contents",
}

// ContextFile is a pre-loaded context file for the system prompt.
type ContextFile struct {
	Path    string
	Content string
}

// BuildSystemPromptOptions configures system prompt construction.
type BuildSystemPromptOptions struct {
	CustomPrompt       string
	SelectedTools      []string
	AppendSystemPrompt string
	Cwd                string
	ContextFiles       []ContextFile
	Skills             []Skill
}

// BuildSystemPrompt constructs the system prompt with tools, guidelines, and context.
func BuildSystemPrompt(opts BuildSystemPromptOptions) string {
	if opts.Cwd == "" {
		opts.Cwd = "."
	}
	if opts.SelectedTools == nil {
		opts.SelectedTools = []string{"read", "bash", "edit", "write"}
	}

	now := time.Now()
	dateTime := now.Format("Monday, January 2, 2006 at 3:04:05 PM MST")

	appendSection := ""
	if opts.AppendSystemPrompt != "" {
		appendSection = "\n\n" + opts.AppendSystemPrompt
	}

	if opts.CustomPrompt != "" {
		return buildCustomPrompt(opts, dateTime, appendSection)
	}

	return buildDefaultPrompt(opts, dateTime, appendSection)
}

func buildCustomPrompt(opts BuildSystemPromptOptions, dateTime, appendSection string) string {
	prompt := opts.CustomPrompt + appendSection

	if len(opts.ContextFiles) > 0 {
		prompt += "\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n"
		for _, cf := range opts.ContextFiles {
			prompt += fmt.Sprintf("## %s\n\n%s\n\n", cf.Path, cf.Content)
		}
	}

	hasRead := len(opts.SelectedTools) == 0
	for _, t := range opts.SelectedTools {
		if t == "read" {
			hasRead = true
			break
		}
	}
	if hasRead && len(opts.Skills) > 0 {
		prompt += FormatSkillsForPrompt(opts.Skills)
	}

	prompt += fmt.Sprintf("\nCurrent date and time: %s", dateTime)
	prompt += fmt.Sprintf("\nCurrent working directory: %s", opts.Cwd)
	return prompt
}

func buildDefaultPrompt(opts BuildSystemPromptOptions, dateTime, appendSection string) string {
	// Filter to known tools
	var tools []string
	for _, t := range opts.SelectedTools {
		if _, ok := ToolDescriptions[t]; ok {
			tools = append(tools, t)
		}
	}

	toolsList := "(none)"
	if len(tools) > 0 {
		var lines []string
		for _, t := range tools {
			lines = append(lines, fmt.Sprintf("- %s: %s", t, ToolDescriptions[t]))
		}
		toolsList = strings.Join(lines, "\n")
	}

	toolSet := make(map[string]bool)
	for _, t := range tools {
		toolSet[t] = true
	}

	// Build guidelines
	var guidelines []string

	if toolSet["bash"] && !toolSet["grep"] && !toolSet["find"] && !toolSet["ls"] {
		guidelines = append(guidelines, "Use bash for file operations like ls, rg, find")
	} else if toolSet["bash"] && (toolSet["grep"] || toolSet["find"] || toolSet["ls"]) {
		guidelines = append(guidelines, "Prefer grep/find/ls tools over bash for file exploration (faster, respects .gitignore)")
	}

	if toolSet["read"] && toolSet["edit"] {
		guidelines = append(guidelines, "Use read to examine files before editing. You must use this tool instead of cat or sed.")
	}
	if toolSet["edit"] {
		guidelines = append(guidelines, "Use edit for precise changes (old text must match exactly)")
	}
	if toolSet["write"] {
		guidelines = append(guidelines, "Use write only for new files or complete rewrites")
	}
	if toolSet["edit"] || toolSet["write"] {
		guidelines = append(guidelines, "When summarizing your actions, output plain text directly - do NOT use cat or bash to display what you did")
	}
	guidelines = append(guidelines, "Be concise in your responses")
	guidelines = append(guidelines, "Show file paths clearly when working with files")

	var guidelineLines []string
	for _, g := range guidelines {
		guidelineLines = append(guidelineLines, "- "+g)
	}
	guidelinesStr := strings.Join(guidelineLines, "\n")

	prompt := fmt.Sprintf(`You are an expert coding assistant operating inside tau, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
%s

In addition to the tools above, you may have access to other custom tools depending on the project.

Guidelines:
%s`, toolsList, guidelinesStr)

	prompt += appendSection

	if len(opts.ContextFiles) > 0 {
		prompt += "\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n"
		for _, cf := range opts.ContextFiles {
			prompt += fmt.Sprintf("## %s\n\n%s\n\n", cf.Path, cf.Content)
		}
	}

	if toolSet["read"] && len(opts.Skills) > 0 {
		prompt += FormatSkillsForPrompt(opts.Skills)
	}

	prompt += fmt.Sprintf("\nCurrent date and time: %s", dateTime)
	prompt += fmt.Sprintf("\nCurrent working directory: %s", opts.Cwd)
	return prompt
}
