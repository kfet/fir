// ACP diff parsing and path utilities.
// Split from helpers.go.
package acp

import (
	"path/filepath"
	"regexp"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// diffLineRegexp matches pi's diff format: prefix + line number + space + content.
var diffLineRegexp = regexp.MustCompile(`^([-+ ])(\s*\d+)\s(.*)$`)

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
