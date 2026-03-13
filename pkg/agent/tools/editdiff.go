// Ported from: packages/coding-agent/src/core/tools/edit-diff.ts
// Upstream hash: 1caadb2e
//
// This file contains diff generation and exported aliases for the
// edit-diff helpers (unexported originals live in edit.go).
package tools

import (
	"fmt"
	"strings"
)

// EditToolDetails contains the diff result from an edit operation.
type EditToolDetails struct {
	Diff             string `json:"diff"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

// EditDiffResult contains the diff string and first changed line.
type EditDiffResult struct {
	Diff             string
	FirstChangedLine *int
}

// GenerateDiffString generates a unified diff with line numbers and context.
func GenerateDiffString(oldContent, newContent string, contextLines int) EditDiffResult {
	if contextLines <= 0 {
		contextLines = 4
	}

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	parts := computeDiffParts(oldLines, newLines)

	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	lineNumWidth := len(fmt.Sprintf("%d", maxLineNum))

	var output []string
	oldLineNum := 1
	newLineNum := 1
	lastWasChange := false
	var firstChangedLine *int

	for i, part := range parts {
		if part.added || part.removed {
			if firstChangedLine == nil {
				n := newLineNum
				firstChangedLine = &n
			}

			for _, line := range part.lines {
				if part.added {
					output = append(output, fmt.Sprintf("+%*d %s", lineNumWidth, newLineNum, line))
					newLineNum++
				} else {
					output = append(output, fmt.Sprintf("-%*d %s", lineNumWidth, oldLineNum, line))
					oldLineNum++
				}
			}
			lastWasChange = true
		} else {
			nextPartIsChange := i < len(parts)-1 && (parts[i+1].added || parts[i+1].removed)

			if lastWasChange || nextPartIsChange {
				linesToShow := part.lines
				skipStart := 0
				skipEnd := 0

				if !lastWasChange {
					skipStart = max(0, len(linesToShow)-contextLines)
					if skipStart > 0 {
						linesToShow = linesToShow[skipStart:]
					}
				}

				if !nextPartIsChange && len(linesToShow) > contextLines {
					skipEnd = len(linesToShow) - contextLines
					linesToShow = linesToShow[:contextLines]
				}

				if skipStart > 0 {
					output = append(output, fmt.Sprintf(" %*s ...", lineNumWidth, ""))
					oldLineNum += skipStart
					newLineNum += skipStart
				}

				for _, line := range linesToShow {
					output = append(output, fmt.Sprintf(" %*d %s", lineNumWidth, oldLineNum, line))
					oldLineNum++
					newLineNum++
				}

				if skipEnd > 0 {
					output = append(output, fmt.Sprintf(" %*s ...", lineNumWidth, ""))
					oldLineNum += skipEnd
					newLineNum += skipEnd
				}
			} else {
				oldLineNum += len(part.lines)
				newLineNum += len(part.lines)
			}

			lastWasChange = false
		}
	}

	return EditDiffResult{
		Diff:             strings.Join(output, "\n"),
		FirstChangedLine: firstChangedLine,
	}
}

// diffPart represents a segment of a line-based diff.
type diffPart struct {
	lines   []string
	added   bool
	removed bool
}

// computeDiffParts computes a simple line-based diff using LCS.
func computeDiffParts(oldLines, newLines []string) []diffPart {
	m, n := len(oldLines), len(newLines)
	// For large files, fall back to a simpler approach
	if m*n > 10_000_000 {
		return simpleDiffParts(oldLines, newLines)
	}

	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var parts []diffPart
	i, j := 0, 0
	for i < m || j < n {
		if i < m && j < n && oldLines[i] == newLines[j] {
			if len(parts) == 0 || parts[len(parts)-1].added || parts[len(parts)-1].removed {
				parts = append(parts, diffPart{})
			}
			parts[len(parts)-1].lines = append(parts[len(parts)-1].lines, oldLines[i])
			i++
			j++
		} else if j < n && (i >= m || lcs[i][j+1] >= lcs[i+1][j]) {
			if len(parts) == 0 || !parts[len(parts)-1].added || parts[len(parts)-1].removed {
				parts = append(parts, diffPart{added: true})
			}
			parts[len(parts)-1].lines = append(parts[len(parts)-1].lines, newLines[j])
			j++
		} else {
			if len(parts) == 0 || !parts[len(parts)-1].removed || parts[len(parts)-1].added {
				parts = append(parts, diffPart{removed: true})
			}
			parts[len(parts)-1].lines = append(parts[len(parts)-1].lines, oldLines[i])
			i++
		}
	}

	return parts
}

// simpleDiffParts is a fallback for very large files.
func simpleDiffParts(oldLines, newLines []string) []diffPart {
	prefixLen := 0
	minLen := len(oldLines)
	if len(newLines) < minLen {
		minLen = len(newLines)
	}
	for prefixLen < minLen && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < minLen-prefixLen &&
		oldLines[len(oldLines)-1-suffixLen] == newLines[len(newLines)-1-suffixLen] {
		suffixLen++
	}

	var parts []diffPart
	if prefixLen > 0 {
		parts = append(parts, diffPart{lines: oldLines[:prefixLen]})
	}

	oldMid := oldLines[prefixLen : len(oldLines)-suffixLen]
	newMid := newLines[prefixLen : len(newLines)-suffixLen]

	if len(oldMid) > 0 {
		parts = append(parts, diffPart{lines: oldMid, removed: true})
	}
	if len(newMid) > 0 {
		parts = append(parts, diffPart{lines: newMid, added: true})
	}

	if suffixLen > 0 {
		parts = append(parts, diffPart{lines: oldLines[len(oldLines)-suffixLen:]})
	}

	return parts
}
