package components

import (
	"sync"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

// OverlayComponent renders a snapshot of pre-built lines in a collapsible box.
// It is a simple lines-holder: the caller builds the pre-formatted, themed
// lines and the component just lays them out. It backs both the session-info
// and help overlays.
type OverlayComponent struct {
	mu    sync.Mutex
	box   *tuicomp.Box
	lines []string
}

// NewOverlayComponent creates a new OverlayComponent from pre-built lines.
func NewOverlayComponent(lines []string) *OverlayComponent {
	t := theme.GetTheme()
	c := &OverlayComponent{
		box:   tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		lines: lines,
	}
	c.rebuildBox()
	return c
}

// SetLines updates the displayed lines and rebuilds the box.
func (c *OverlayComponent) SetLines(lines []string) {
	c.mu.Lock()
	c.lines = lines
	c.rebuildBox()
	c.mu.Unlock()
}

// Invalidate rebuilds the display.
func (c *OverlayComponent) Invalidate() {
	c.mu.Lock()
	c.box.Invalidate()
	c.rebuildBox()
	c.mu.Unlock()
}

// Render renders the overlay component. Thread-safe.
func (c *OverlayComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.box.Render(width)
}

// rebuildBox rebuilds all Box children from current state.
// Must be called with c.mu held.
func (c *OverlayComponent) rebuildBox() {
	c.box.Clear()
	for _, line := range c.lines {
		c.box.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}

// Ensure OverlayComponent implements tui.Component.
var _ tui.Component = (*OverlayComponent)(nil)
