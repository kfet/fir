// Ported from: packages/coding-agent/src/utils/frontmatter.ts
// Upstream hash: 1caadb2e
package core

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ParsedFrontmatter holds parsed frontmatter and body content.
type ParsedFrontmatter struct {
	Frontmatter map[string]any
	Body        string
}

// ParseFrontmatter extracts YAML frontmatter and body from markdown content.
// Frontmatter is delimited by --- at the start and end.
func ParseFrontmatter(content string) ParsedFrontmatter {
	normalized := normalizeNewlinesForFM(content)
	empty := ParsedFrontmatter{Frontmatter: map[string]any{}, Body: normalized}

	if !strings.HasPrefix(normalized, "---") {
		return empty
	}

	// Content starts with "---", must be followed by newline
	rest := normalized[3:]
	if len(rest) == 0 || rest[0] != '\n' {
		return empty
	}
	rest = rest[1:] // skip the newline after opening ---

	// Find closing "---" at the start of a line
	// It could be at position 0 (empty frontmatter) or after a newline
	closingIdx := -1
	if strings.HasPrefix(rest, "---") {
		closingIdx = 0
	} else {
		idx := strings.Index(rest, "\n---")
		if idx != -1 {
			closingIdx = idx + 1 // point to the first '-'
		}
	}

	if closingIdx == -1 {
		return empty
	}

	yamlStr := rest[:closingIdx]
	// Body starts after "---" plus optional newline
	afterClose := rest[closingIdx+3:]
	body := ""
	if len(afterClose) > 0 {
		if afterClose[0] == '\n' {
			afterClose = afterClose[1:]
		}
		body = strings.TrimSpace(afterClose)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil || parsed == nil {
		parsed = map[string]any{}
	}

	return ParsedFrontmatter{
		Frontmatter: parsed,
		Body:        body,
	}
}

// StripFrontmatter returns the content without frontmatter.
func StripFrontmatter(content string) string {
	return ParseFrontmatter(content).Body
}

// normalizeNewlinesForFM converts \r\n and \r to \n.
func normalizeNewlinesForFM(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
