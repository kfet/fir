// Ported from: packages/coding-agent/src/modes/interactive/components/diff.ts
// Upstream hash: 3b1f8e5d
package components

import (
	"regexp"
	"strings"

	"github.com/kfet/tau/pkg/modes/interactive/theme"
)

var diffLineRe = regexp.MustCompile(`^([+ -])(\s*\d*)\s(.*)$`)

// parseDiffLine parses a diff line to extract prefix, line number, and content.
func parseDiffLine(line string) (prefix, lineNum, content string, ok bool) {
	m := diffLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// replaceTabs replaces tabs with spaces for consistent rendering.
func replaceTabs(text string) string {
	return strings.ReplaceAll(text, "\t", "   ")
}

// renderIntraLineDiff computes word-level diff and renders with inverse on changed parts.
// Uses a simple word-level diff since Go doesn't have a direct equivalent of `diffWords`.
func renderIntraLineDiff(t *theme.Theme, oldContent, newContent string) (removedLine, addedLine string) {
	// Simple approach: find common prefix and suffix, highlight the difference
	oldWords := splitWords(oldContent)
	newWords := splitWords(newContent)

	// Find common prefix length
	prefixLen := 0
	for prefixLen < len(oldWords) && prefixLen < len(newWords) && oldWords[prefixLen] == newWords[prefixLen] {
		prefixLen++
	}

	// Find common suffix length
	suffixLen := 0
	for suffixLen < len(oldWords)-prefixLen && suffixLen < len(newWords)-prefixLen &&
		oldWords[len(oldWords)-1-suffixLen] == newWords[len(newWords)-1-suffixLen] {
		suffixLen++
	}

	// Build removed line
	removedLine = strings.Join(oldWords[:prefixLen], "")
	if prefixLen < len(oldWords)-suffixLen {
		changed := strings.Join(oldWords[prefixLen:len(oldWords)-suffixLen], "")
		removedLine += t.Inverse(changed)
	}
	if suffixLen > 0 {
		removedLine += strings.Join(oldWords[len(oldWords)-suffixLen:], "")
	}

	// Build added line
	addedLine = strings.Join(newWords[:prefixLen], "")
	if prefixLen < len(newWords)-suffixLen {
		changed := strings.Join(newWords[prefixLen:len(newWords)-suffixLen], "")
		addedLine += t.Inverse(changed)
	}
	if suffixLen > 0 {
		addedLine += strings.Join(newWords[len(newWords)-suffixLen:], "")
	}

	return removedLine, addedLine
}

// splitWords splits text into word-like tokens (word + following whitespace).
func splitWords(text string) []string {
	var words []string
	i := 0
	for i < len(text) {
		// Find word boundary
		j := i
		for j < len(text) && text[j] != ' ' && text[j] != '\t' {
			j++
		}
		// Include trailing whitespace
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j > i {
			words = append(words, text[i:j])
		}
		i = j
	}
	return words
}

// RenderDiffOptions are options for RenderDiff.
type RenderDiffOptions struct {
	FilePath string // unused, kept for API compatibility
}

// RenderDiff renders a diff string with colored lines and intra-line change highlighting.
func RenderDiff(diffText string, opts *RenderDiffOptions) string {
	t := theme.GetTheme()
	lines := strings.Split(diffText, "\n")
	var result []string

	i := 0
	for i < len(lines) {
		line := lines[i]
		prefix, lineNum, content, ok := parseDiffLine(line)

		if !ok {
			result = append(result, t.Fg("toolDiffContext", line))
			i++
			continue
		}

		if prefix == "-" {
			// Collect consecutive removed lines
			type parsed struct {
				lineNum, content string
			}
			var removedLines []parsed
			for i < len(lines) {
				p, ln, c, ok := parseDiffLine(lines[i])
				if !ok || p != "-" {
					break
				}
				removedLines = append(removedLines, parsed{ln, c})
				i++
			}

			// Collect consecutive added lines
			var addedLines []parsed
			for i < len(lines) {
				p, ln, c, ok := parseDiffLine(lines[i])
				if !ok || p != "+" {
					break
				}
				addedLines = append(addedLines, parsed{ln, c})
				i++
			}

			// Only do intra-line diffing when there's exactly one removed and one added line
			if len(removedLines) == 1 && len(addedLines) == 1 {
				removed := removedLines[0]
				added := addedLines[0]
				removedLine, addedLine := renderIntraLineDiff(t,
					replaceTabs(removed.content), replaceTabs(added.content))
				result = append(result, t.Fg("toolDiffRemoved", "-"+removed.lineNum+" "+removedLine))
				result = append(result, t.Fg("toolDiffAdded", "+"+added.lineNum+" "+addedLine))
			} else {
				for _, removed := range removedLines {
					result = append(result, t.Fg("toolDiffRemoved", "-"+removed.lineNum+" "+replaceTabs(removed.content)))
				}
				for _, added := range addedLines {
					result = append(result, t.Fg("toolDiffAdded", "+"+added.lineNum+" "+replaceTabs(added.content)))
				}
			}
		} else if prefix == "+" {
			result = append(result, t.Fg("toolDiffAdded", "+"+lineNum+" "+replaceTabs(content)))
			i++
		} else {
			result = append(result, t.Fg("toolDiffContext", " "+lineNum+" "+replaceTabs(content)))
			i++
		}
	}

	return strings.Join(result, "\n")
}
