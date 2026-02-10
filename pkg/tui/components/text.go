// Ported from: packages/tui/src/components/text.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"

	"github.com/kfet/pi-go/pkg/tui"
)

// Text displays multi-line text with word wrapping.
type Text struct {
	text       string
	paddingX   int
	paddingY   int
	customBgFn func(string) string

	cachedText  *string
	cachedWidth *int
	cachedLines []string
}

// NewText creates a Text component.
func NewText(text string, paddingX, paddingY int, customBgFn func(string) string) *Text {
	return &Text{
		text:       text,
		paddingX:   paddingX,
		paddingY:   paddingY,
		customBgFn: customBgFn,
	}
}

// SetText updates the displayed text and invalidates the cache.
func (t *Text) SetText(text string) {
	t.text = text
	t.cachedText = nil
	t.cachedWidth = nil
	t.cachedLines = nil
}

// SetCustomBgFn sets a custom background function.
func (t *Text) SetCustomBgFn(fn func(string) string) {
	t.customBgFn = fn
	t.cachedText = nil
	t.cachedWidth = nil
	t.cachedLines = nil
}

// Invalidate clears any cached rendering state.
func (t *Text) Invalidate() {
	t.cachedText = nil
	t.cachedWidth = nil
	t.cachedLines = nil
}

// Render renders the text component to lines for the given width.
func (t *Text) Render(width int) []string {
	// Check cache
	if t.cachedLines != nil && t.cachedText != nil && *t.cachedText == t.text &&
		t.cachedWidth != nil && *t.cachedWidth == width {
		return t.cachedLines
	}

	// Don't render anything if there's no actual text
	if t.text == "" || strings.TrimSpace(t.text) == "" {
		result := []string{}
		t.cachedText = &t.text
		t.cachedWidth = &width
		t.cachedLines = result
		return result
	}

	// Replace tabs with 3 spaces
	normalizedText := strings.ReplaceAll(t.text, "\t", "   ")

	// Calculate content width (subtract left/right margins)
	contentWidth := width - t.paddingX*2
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Wrap text (preserves ANSI codes)
	wrappedLines := tui.WrapTextWithAnsi(normalizedText, contentWidth)

	// Add margins and background to each line
	leftMargin := strings.Repeat(" ", t.paddingX)
	rightMargin := strings.Repeat(" ", t.paddingX)
	var contentLines []string

	for _, line := range wrappedLines {
		lineWithMargins := leftMargin + line + rightMargin

		if t.customBgFn != nil {
			contentLines = append(contentLines, tui.ApplyBackgroundToLine(lineWithMargins, width, t.customBgFn))
		} else {
			visLen := tui.VisibleWidth(lineWithMargins)
			paddingNeeded := width - visLen
			if paddingNeeded < 0 {
				paddingNeeded = 0
			}
			contentLines = append(contentLines, lineWithMargins+strings.Repeat(" ", paddingNeeded))
		}
	}

	// Add top/bottom padding (empty lines)
	emptyLine := strings.Repeat(" ", width)
	var emptyLines []string
	for i := 0; i < t.paddingY; i++ {
		if t.customBgFn != nil {
			emptyLines = append(emptyLines, tui.ApplyBackgroundToLine(emptyLine, width, t.customBgFn))
		} else {
			emptyLines = append(emptyLines, emptyLine)
		}
	}

	var result []string
	result = append(result, emptyLines...)
	result = append(result, contentLines...)
	result = append(result, emptyLines...)

	// Update cache
	txt := t.text
	t.cachedText = &txt
	w := width
	t.cachedWidth = &w
	t.cachedLines = result

	if len(result) > 0 {
		return result
	}
	return []string{""}
}
