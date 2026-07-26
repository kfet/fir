package components

import (
	"strings"

	"github.com/kfet/tui"
)

// RoundedBorder wraps child components in a rounded border box (╭─╮│╰─╯).
type RoundedBorder struct {
	children []tui.Component
	colorFn  func(string) string
	paddingX int
}

var _ tui.Component = (*RoundedBorder)(nil)

// NewRoundedBorder creates a RoundedBorder with the given border color function
// and inner horizontal padding.
func NewRoundedBorder(colorFn func(string) string, paddingX int) *RoundedBorder {
	if colorFn == nil {
		colorFn = func(s string) string { return s }
	}
	return &RoundedBorder{colorFn: colorFn, paddingX: paddingX}
}

// AddChild adds a component inside the border.
func (r *RoundedBorder) AddChild(c tui.Component) {
	r.children = append(r.children, c)
}

// Invalidate propagates to children.
func (r *RoundedBorder) Invalidate() {
	for _, c := range r.children {
		c.Invalidate()
	}
}

// Render renders the children wrapped in a rounded border.
func (r *RoundedBorder) Render(width int) []string {
	if width < 4 {
		width = 4
	}

	// Border uses 2 chars (left + right), padding uses paddingX*2.
	innerWidth := width - 2 - r.paddingX*2
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Render children at inner width.
	var childLines []string
	for _, c := range r.children {
		childLines = append(childLines, c.Render(innerWidth)...)
	}

	pad := strings.Repeat(" ", r.paddingX)
	barWidth := width - 2 // chars between corners
	topBar := strings.Repeat("─", barWidth)
	botBar := strings.Repeat("─", barWidth)

	var out []string
	out = append(out, r.colorFn("╭"+topBar+"╮"))

	for _, line := range childLines {
		// Pad line to innerWidth accounting for ANSI codes.
		visible := tui.VisibleWidth(line)
		trailing := ""
		if visible < innerWidth {
			trailing = strings.Repeat(" ", innerWidth-visible)
		}
		out = append(out, r.colorFn("│")+pad+line+trailing+pad+r.colorFn("│"))
	}

	out = append(out, r.colorFn("╰"+botBar+"╯"))
	return out
}
