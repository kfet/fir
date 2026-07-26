package components

import (
	"strings"

	"github.com/kfet/tui"
)

// Indented wraps a component with left-side indentation.
type Indented struct {
	child  tui.Component
	indent int
}

var _ tui.Component = (*Indented)(nil)

// NewIndented creates an Indented wrapper with the given left indent.
func NewIndented(child tui.Component, indent int) *Indented {
	return &Indented{child: child, indent: indent}
}

// Invalidate propagates to the child.
func (ind *Indented) Invalidate() {
	ind.child.Invalidate()
}

// Render renders the child at (width - indent), then prepends spaces.
func (ind *Indented) Render(width int) []string {
	innerWidth := width - ind.indent
	if innerWidth < 1 {
		innerWidth = 1
	}
	lines := ind.child.Render(innerWidth)
	prefix := strings.Repeat(" ", ind.indent)
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = ""
		} else {
			out[i] = prefix + line
		}
	}
	return out
}
