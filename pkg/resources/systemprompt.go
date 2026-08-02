// Ported from: packages/coding-agent/src/core/system-prompt.ts
// Upstream hash: a1edb8a4
package resources

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

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
	// Date overrides the "Current date" line in the system prompt.
	// If empty, defaults to time.Now() formatted as YYYY-MM-DD.
	// Set this once per session to avoid cache-breaking date changes at midnight.
	Date string
	// MCPToolTimeout, when > 0, advertises the default per-call timeout applied
	// to MCP tools. Set only when MCP tools are present so the block is omitted
	// otherwise. Zero means "no MCP tools / bound disabled" — emit nothing.
	MCPToolTimeout time.Duration
}

// BuildSystemPrompt constructs the system prompt with tools, guidelines, and context.
func BuildSystemPrompt(opts BuildSystemPromptOptions) string {
	if opts.Cwd == "" {
		opts.Cwd = "."
	}
	// Normalize backslashes to forward slashes (Windows paths).
	promptCwd := filepath.ToSlash(opts.Cwd)

	if opts.SelectedTools == nil {
		opts.SelectedTools = []string{"read", "bash", "edit", "write"}
	}

	date := opts.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	appendSection := ""
	if opts.AppendSystemPrompt != "" {
		appendSection = "\n\n" + opts.AppendSystemPrompt
	}

	if opts.CustomPrompt != "" {
		return buildCustomPrompt(opts, promptCwd, date, appendSection)
	}

	return buildDefaultPrompt(opts, promptCwd, date, appendSection)
}

func buildCustomPrompt(opts BuildSystemPromptOptions, promptCwd, date, appendSection string) string {
	prompt := opts.CustomPrompt + appendSection

	if len(opts.ContextFiles) > 0 {
		prompt += "\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n"
		for _, cf := range opts.ContextFiles {
			prompt += fmt.Sprintf("## %s\n\n%s\n\n", cf.Path, cf.Content)
		}
	}

	hasRead := len(opts.SelectedTools) == 0 || slices.Contains(opts.SelectedTools, "read")
	if hasRead && len(opts.Skills) > 0 {
		prompt += FormatSkillsForPrompt(opts.Skills)
	}

	prompt += fmt.Sprintf("\nCurrent date: %s", date)
	prompt += fmt.Sprintf("\nCurrent working directory: %s", promptCwd)
	return prompt
}

func buildDefaultPrompt(opts BuildSystemPromptOptions, promptCwd, date, appendSection string) string {
	toolSet := make(map[string]bool)
	for _, t := range opts.SelectedTools {
		toolSet[t] = true
	}

	// Build guidelines
	var guidelines []string

	if toolSet["bash"] && !toolSet["grep"] && !toolSet["find"] {
		guidelines = append(guidelines, "Use bash for file operations like ls, rg, find")
	} else if toolSet["bash"] && (toolSet["grep"] || toolSet["find"]) {
		guidelines = append(guidelines, "Prefer grep/find tools over bash for search and file discovery (faster; grep respects .gitignore)")
	}

	guidelines = append(guidelines, "Be concise in your responses")
	guidelines = append(guidelines, "Show file paths clearly when working with files")
	if toolSet["plan"] {
		guidelines = append(guidelines, "For non-trivial tasks, use the plan tool to break work into steps and track progress")
	}
	if opts.MCPToolTimeout > 0 {
		guidelines = append(guidelines, fmt.Sprintf("MCP (mcp__*) tool calls have a default timeout of %s; a call that exceeds it returns a timeout error you can act on (retry, try another approach, or ask the user to raise mcp.toolTimeoutSeconds). This bounds wall-clock time, not silence.", opts.MCPToolTimeout))
	}

	var guidelineLines []string
	for _, g := range guidelines {
		guidelineLines = append(guidelineLines, "- "+g)
	}
	guidelinesStr := strings.Join(guidelineLines, "\n")

	prompt := fmt.Sprintf(`You are an expert coding assistant operating inside fir, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Guidelines:
%s`, guidelinesStr)

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

	prompt += fmt.Sprintf("\nCurrent date: %s", date)
	prompt += fmt.Sprintf("\nCurrent working directory: %s", promptCwd)
	return prompt
}
