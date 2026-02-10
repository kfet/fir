// Ported from: packages/coding-agent/src/modes/interactive/components/visual-truncate.ts
// Upstream hash: 4a7b1e3c
package components

import (
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// VisualTruncateResult holds the result of truncating text to visual lines.
type VisualTruncateResult struct {
	// VisualLines are the visual lines to display.
	VisualLines []string
	// SkippedCount is the number of visual lines that were hidden.
	SkippedCount int
}

// TruncateToVisualLines truncates text to a maximum number of visual lines (from the end).
// This accounts for line wrapping based on terminal width.
//
// paddingX is horizontal padding for the Text component (default 0).
// Use 0 when result will be placed in a Box (Box adds its own padding).
// Use 1 when result will be placed in a plain Container.
func TruncateToVisualLines(text string, maxVisualLines, width, paddingX int) VisualTruncateResult {
	if text == "" {
		return VisualTruncateResult{VisualLines: nil, SkippedCount: 0}
	}

	// Create a temporary Text component to render and get visual lines
	tempText := tuicomp.NewText(text, paddingX, 0, nil)
	allVisualLines := tempText.Render(width)

	if len(allVisualLines) <= maxVisualLines {
		return VisualTruncateResult{VisualLines: allVisualLines, SkippedCount: 0}
	}

	// Take the last N visual lines
	truncatedLines := allVisualLines[len(allVisualLines)-maxVisualLines:]
	skippedCount := len(allVisualLines) - maxVisualLines

	return VisualTruncateResult{VisualLines: truncatedLines, SkippedCount: skippedCount}
}
