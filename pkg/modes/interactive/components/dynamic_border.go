// Ported from: packages/coding-agent/src/modes/interactive/components/dynamic-border.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"

	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
)

// DynamicBorder renders a horizontal border line that adjusts to viewport width.
type DynamicBorder struct {
	color func(string) string
}

// NewDynamicBorder creates a DynamicBorder with an optional color function.
// If colorFn is nil, uses the global theme's "border" color.
func NewDynamicBorder(colorFn func(string) string) *DynamicBorder {
	if colorFn == nil {
		colorFn = func(s string) string {
			return theme.GetTheme().Fg("border", s)
		}
	}
	return &DynamicBorder{color: colorFn}
}

// Invalidate is a no-op.
func (d *DynamicBorder) Invalidate() {}

// Render renders a horizontal border line.
func (d *DynamicBorder) Render(width int) []string {
	w := width
	if w < 1 {
		w = 1
	}
	return []string{d.color(strings.Repeat("─", w))}
}
