// Ported from: packages/coding-agent/src/core/prompt-templates.ts
// Upstream hash: 1caadb2e
package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kfet/fir/pkg/config"
)


// PromptTemplate represents a prompt template loaded from a markdown file.
type PromptTemplate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Source      string `json:"source"`   // "user", "project", or "path"
	FilePath    string `json:"filePath"` // Absolute path to the template file
}

// ParseCommandArgs parses command arguments respecting quoted strings (bash-style).
func ParseCommandArgs(argsString string) []string {
	var args []string
	var current strings.Builder
	var inQuote rune

	for _, ch := range argsString {
		if inQuote != 0 {
			if ch == inQuote {
				inQuote = 0
			} else {
				current.WriteRune(ch)
			}
		} else if ch == '"' || ch == '\'' {
			inQuote = ch
		} else if ch == ' ' || ch == '\t' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}

// positionalArgRe matches $1, $2, etc.
var positionalArgRe = regexp.MustCompile(`\$(\d+)`)

// sliceArgRe matches ${@:start} or ${@:start:length}
var sliceArgRe = regexp.MustCompile(`\$\{@:(\d+)(?::(\d+))?\}`)

// SubstituteArgs replaces argument placeholders in template content.
// Supports: $1, $2, ...; $@ and $ARGUMENTS for all args;
// ${@:N} for args from Nth onwards; ${@:N:L} for L args from Nth.
func SubstituteArgs(content string, args []string) string {
	result := content

	// Replace $1, $2, etc. first (before wildcards)
	result = positionalArgRe.ReplaceAllStringFunc(result, func(match string) string {
		numStr := match[1:] // strip leading $
		num, _ := strconv.Atoi(numStr)
		idx := num - 1
		if idx >= 0 && idx < len(args) {
			return args[idx]
		}
		return ""
	})

	// Replace ${@:start} or ${@:start:length}
	result = sliceArgRe.ReplaceAllStringFunc(result, func(match string) string {
		submatch := sliceArgRe.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		start, _ := strconv.Atoi(submatch[1])
		start-- // convert to 0-indexed
		if start < 0 {
			start = 0
		}

		if start >= len(args) {
			return ""
		}

		if len(submatch) >= 3 && submatch[2] != "" {
			length, _ := strconv.Atoi(submatch[2])
			end := start + length
			if end > len(args) {
				end = len(args)
			}
			return strings.Join(args[start:end], " ")
		}
		return strings.Join(args[start:], " ")
	})

	// Pre-compute all args joined
	allArgs := strings.Join(args, " ")

	// Replace $ARGUMENTS with all args joined
	result = strings.ReplaceAll(result, "$ARGUMENTS", allArgs)

	// Replace $@ with all args joined
	result = strings.ReplaceAll(result, "$@", allArgs)

	return result
}

// loadTemplateFromFile loads a single prompt template from a markdown file.
func loadTemplateFromFile(filePath, source, sourceLabel string) *PromptTemplate {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}

	parsed := ParseFrontmatter(string(data))

	name := strings.TrimSuffix(filepath.Base(filePath), ".md")

	// Get description from frontmatter or first non-empty line
	description := ""
	if desc, ok := parsed.Frontmatter["description"].(string); ok {
		description = desc
	}
	if description == "" {
		for _, line := range strings.Split(parsed.Body, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				if len(trimmed) > 60 {
					description = trimmed[:60] + "..."
				} else {
					description = trimmed
				}
				break
			}
		}
	}

	// Append source label
	if description != "" {
		description = description + " " + sourceLabel
	} else {
		description = sourceLabel
	}

	return &PromptTemplate{
		Name:        name,
		Description: description,
		Content:     parsed.Body,
		Source:      source,
		FilePath:    filePath,
	}
}

// loadTemplatesFromDir scans a directory for .md files and loads them.
func loadTemplatesFromDir(dir, source, sourceLabel string) []PromptTemplate {
	var templates []PromptTemplate

	entries, err := os.ReadDir(dir)
	if err != nil {
		return templates
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())

		// Handle symlinks
		isFile := !entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(fullPath)
			if err != nil {
				continue
			}
			isFile = !info.IsDir()
		}

		if isFile && strings.HasSuffix(entry.Name(), ".md") {
			if tmpl := loadTemplateFromFile(fullPath, source, sourceLabel); tmpl != nil {
				templates = append(templates, *tmpl)
			}
		}
	}

	return templates
}

// LoadPromptTemplatesOptions configures template loading.
type LoadPromptTemplatesOptions struct {
	Cwd             string   // Working directory for project-local templates
	AgentDir        string   // Agent config directory for global templates
	PromptPaths     []string // Explicit prompt template paths (files or directories)
	IncludeDefaults bool     // Include default prompt directories (default: true)
}

// LoadPromptTemplates loads all prompt templates from configured locations.
func LoadPromptTemplates(opts LoadPromptTemplatesOptions) []PromptTemplate {
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	var templates []PromptTemplate

	if opts.IncludeDefaults {
		// Global templates from agentDir/prompts/
		if opts.AgentDir != "" {
			globalDir := filepath.Join(opts.AgentDir, "prompts")
			templates = append(templates, loadTemplatesFromDir(globalDir, "user", "(user)")...)
		}

		// Project templates from cwd/.fir/prompts/
		projectDir := filepath.Join(cwd, config.ConfigDirName, "prompts")
		templates = append(templates, loadTemplatesFromDir(projectDir, "project", "(project)")...)
	}

	// Compute known dirs for source inference when explicit paths overlap defaults
	userPromptsDir := ""
	if opts.AgentDir != "" {
		userPromptsDir = filepath.Join(opts.AgentDir, "prompts")
	}
	projectPromptsDir := filepath.Join(cwd, config.ConfigDirName, "prompts")

	// Explicit prompt paths
	for _, rawPath := range opts.PromptPaths {
		resolvedPath := resolvePromptPath(rawPath, cwd)
		info, err := os.Stat(resolvedPath)
		if err != nil {
			continue
		}

		// Infer source from path location (matches upstream behavior)
		source, label := inferPromptSource(resolvedPath, userPromptsDir, projectPromptsDir)

		if info.IsDir() {
			templates = append(templates, loadTemplatesFromDir(resolvedPath, source, label)...)
		} else if strings.HasSuffix(resolvedPath, ".md") {
			if tmpl := loadTemplateFromFile(resolvedPath, source, label); tmpl != nil {
				templates = append(templates, *tmpl)
			}
		}
	}

	return templates
}

// resolvePromptPath resolves a prompt path relative to cwd, with ~ expansion.
func resolvePromptPath(p, cwd string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// inferPromptSource determines the source and label for a prompt path
// based on whether it falls under the user or project prompts directory.
func inferPromptSource(resolvedPath, userPromptsDir, projectPromptsDir string) (source, label string) {
	if userPromptsDir != "" && isPathUnder(resolvedPath, userPromptsDir) {
		return "user", "(user)"
	}
	if projectPromptsDir != "" && isPathUnder(resolvedPath, projectPromptsDir) {
		return "project", "(project)"
	}
	return "path", "(path:" + strings.TrimSuffix(filepath.Base(resolvedPath), ".md") + ")"
}

// isPathUnder checks if target is equal to or under root.
func isPathUnder(target, root string) bool {
	if target == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

// ExpandPromptTemplate expands a prompt template if the text matches a template name.
// Returns the expanded content or the original text if not a template.
func ExpandPromptTemplate(text string, templates []PromptTemplate) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}

	spaceIdx := strings.Index(text, " ")
	var templateName, argsString string
	if spaceIdx == -1 {
		templateName = text[1:]
		argsString = ""
	} else {
		templateName = text[1:spaceIdx]
		argsString = text[spaceIdx+1:]
	}

	for _, tmpl := range templates {
		if tmpl.Name == templateName {
			args := ParseCommandArgs(argsString)
			return SubstituteArgs(tmpl.Content, args)
		}
	}

	return text
}
