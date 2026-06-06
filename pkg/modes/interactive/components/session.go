package components

import (
	"sync"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// SessionComponent renders a snapshot of session info in a collapsible box.
// It is a simple lines-holder: the caller builds the pre-formatted, themed
// lines and the component just lays them out.
type SessionComponent struct {
	mu    sync.Mutex
	box   *tuicomp.Box
	lines []string
}

// NewSessionComponent creates a new SessionComponent from pre-built lines.
func NewSessionComponent(lines []string) *SessionComponent {
	t := theme.GetTheme()
	c := &SessionComponent{
		box:   tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		lines: lines,
	}
	c.rebuildBox()
	return c
}

// SetLines updates the displayed lines and rebuilds the box.
func (c *SessionComponent) SetLines(lines []string) {
	c.mu.Lock()
	c.lines = lines
	c.rebuildBox()
	c.mu.Unlock()
}

// Invalidate rebuilds the display.
func (c *SessionComponent) Invalidate() {
	c.mu.Lock()
	c.box.Invalidate()
	c.rebuildBox()
	c.mu.Unlock()
}

// Render renders the session component. Thread-safe.
func (c *SessionComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.box.Render(width)
}

// rebuildBox rebuilds all Box children from current state.
// Must be called with c.mu held.
func (c *SessionComponent) rebuildBox() {
	c.box.Clear()
	for _, line := range c.lines {
		c.box.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}

// Ensure SessionComponent implements tui.Component.
var _ tui.Component = (*SessionComponent)(nil)
