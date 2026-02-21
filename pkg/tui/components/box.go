// Ported from: packages/tui/src/components/box.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"

	"github.com/kfet/fir/pkg/tui"
)

// Box is a container that applies padding and background to all children.
type Box struct {
	Children []tui.Component
	paddingX int
	paddingY int
	bgFn     func(string) string

	// Cache
	cachedChildLines []string
	cachedWidth      int
	cachedBgSample   string
	cachedLines      []string
	cacheValid       bool
}

var _ tui.Component = (*Box)(nil)

// NewBox creates a Box with the given padding and optional background function.
func NewBox(paddingX, paddingY int, bgFn func(string) string) *Box {
	return &Box{
		paddingX: paddingX,
		paddingY: paddingY,
		bgFn:     bgFn,
	}
}

// AddChild adds a component to the box.
func (b *Box) AddChild(c tui.Component) {
	b.Children = append(b.Children, c)
	b.cacheValid = false
}

// RemoveChild removes a component from the box.
func (b *Box) RemoveChild(c tui.Component) {
	for i, child := range b.Children {
		if child == c {
			b.Children = append(b.Children[:i], b.Children[i+1:]...)
			b.cacheValid = false
			return
		}
	}
}

// Clear removes all children.
func (b *Box) Clear() {
	b.Children = nil
	b.cacheValid = false
}

// SetBgFn sets the background function.
func (b *Box) SetBgFn(bgFn func(string) string) {
	b.bgFn = bgFn
}

// Invalidate clears cache and invalidates all children.
func (b *Box) Invalidate() {
	b.cacheValid = false
	for _, child := range b.Children {
		child.Invalidate()
	}
}

// Render renders all children within the box with padding and background.
func (b *Box) Render(width int) []string {
	if len(b.Children) == 0 {
		return nil
	}

	contentWidth := width - b.paddingX*2
	if contentWidth < 1 {
		contentWidth = 1
	}
	leftPad := strings.Repeat(" ", b.paddingX)

	// Render children
	var childLines []string
	for _, child := range b.Children {
		lines := child.Render(contentWidth)
		for _, line := range lines {
			childLines = append(childLines, leftPad+line)
		}
	}

	if len(childLines) == 0 {
		return nil
	}

	// Check bgFn sample
	bgSample := ""
	if b.bgFn != nil {
		bgSample = b.bgFn("test")
	}

	// Check cache
	if b.cacheValid && b.cachedWidth == width && b.cachedBgSample == bgSample && b.matchChildLines(childLines) {
		return b.cachedLines
	}

	// Build result
	result := make([]string, 0, len(childLines)+b.paddingY*2)

	for i := 0; i < b.paddingY; i++ {
		result = append(result, b.applyBg("", width))
	}
	for _, line := range childLines {
		result = append(result, b.applyBg(line, width))
	}
	for i := 0; i < b.paddingY; i++ {
		result = append(result, b.applyBg("", width))
	}

	// Update cache
	b.cachedChildLines = childLines
	b.cachedWidth = width
	b.cachedBgSample = bgSample
	b.cachedLines = result
	b.cacheValid = true

	return result
}

func (b *Box) matchChildLines(lines []string) bool {
	if len(b.cachedChildLines) != len(lines) {
		return false
	}
	for i, l := range b.cachedChildLines {
		if l != lines[i] {
			return false
		}
	}
	return true
}

func (b *Box) applyBg(line string, width int) string {
	visLen := tui.VisibleWidth(line)
	padNeeded := width - visLen
	if padNeeded < 0 {
		padNeeded = 0
	}
	padded := line + strings.Repeat(" ", padNeeded)

	if b.bgFn != nil {
		return tui.ApplyBackgroundToLine(padded, width, b.bgFn)
	}
	return padded
}
