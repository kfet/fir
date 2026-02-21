// Ported from: packages/coding-agent/src/modes/interactive/components/login-dialog.ts
// Upstream hash: 6e7f8a9b
package components

import (
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// LoginDialogComponent handles the OAuth login flow.
type LoginDialogComponent struct {
	tui.Container
	contentContainer *tui.Container
	input            *tuicomp.Input
	ui               *tui.TUI
	providerID       string
	onComplete       func(success bool, message string)
	cancelled        bool
}

// NewLoginDialogComponent creates a new login dialog.
func NewLoginDialogComponent(
	ui *tui.TUI,
	providerID string,
	providerName string,
	onComplete func(success bool, message string),
) *LoginDialogComponent {
	t := theme.GetTheme()
	c := &LoginDialogComponent{
		ui:         ui,
		providerID: providerID,
		onComplete: onComplete,
	}

	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewText(t.Fg("warning", "Login to "+providerName), 1, 0, nil))

	c.contentContainer = &tui.Container{}
	c.AddChild(c.contentContainer)

	c.input = tuicomp.NewInput()
	c.input.OnSubmit = func(text string) {}
	c.input.OnEscape = func() { c.Cancel() }

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// Cancel cancels the login flow.
func (c *LoginDialogComponent) Cancel() {
	if c.cancelled {
		return
	}
	c.cancelled = true
	c.onComplete(false, "Login cancelled")
}

// HandleInput processes keyboard input.
func (c *LoginDialogComponent) HandleInput(data string) {
	if c.input != nil {
		c.input.HandleInput(data)
	}
}

// SetMessage sets a message to display.
func (c *LoginDialogComponent) SetMessage(msg string) {
	t := theme.GetTheme()
	c.contentContainer.Clear()
	c.contentContainer.AddChild(tuicomp.NewText(t.Fg("muted", msg), 1, 0, nil))
}
