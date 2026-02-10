// Ported from: packages/coding-agent/src/modes/interactive/components/extension-input.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"

	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// ExtensionInputOptions configures an ExtensionInputComponent.
type ExtensionInputOptions struct {
	TUI     *tui.TUI
	Timeout int // timeout in milliseconds, 0 = no timeout
}

// ExtensionInputComponent is a simple text input component for extensions.
type ExtensionInputComponent struct {
	tui.Container
	input     *tuicomp.Input
	onSubmit  func(value string)
	onCancel  func()
	titleText *tuicomp.Text
	baseTitle string
	countdown *CountdownTimer
	focused   bool
}

var _ tui.Component = (*ExtensionInputComponent)(nil)
var _ tui.InputHandler = (*ExtensionInputComponent)(nil)
var _ tui.Focusable = (*ExtensionInputComponent)(nil)

// NewExtensionInputComponent creates a new ExtensionInputComponent.
func NewExtensionInputComponent(title string, placeholder string, onSubmit func(string), onCancel func(), opts *ExtensionInputOptions) *ExtensionInputComponent {
	t := theme.GetTheme()

	c := &ExtensionInputComponent{
		onSubmit:  onSubmit,
		onCancel:  onCancel,
		baseTitle: title,
	}

	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.titleText = tuicomp.NewText(t.Fg("accent", title), 1, 0, nil)
	c.AddChild(c.titleText)
	c.AddChild(tuicomp.NewSpacer(1))

	if opts != nil && opts.Timeout > 0 && opts.TUI != nil {
		c.countdown = NewCountdownTimer(opts.Timeout, opts.TUI,
			func(s int) {
				c.titleText.SetText(t.Fg("accent", fmt.Sprintf("%s (%ds)", c.baseTitle, s)))
			},
			func() { c.onCancel() },
		)
	}

	c.input = tuicomp.NewInput()
	c.AddChild(c.input)
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(tuicomp.NewText(
		KeyHint("selectConfirm", "submit")+"  "+KeyHint("selectCancel", "cancel"),
		1, 0, nil,
	))
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// SetFocused propagates focus to the input for cursor positioning.
func (c *ExtensionInputComponent) SetFocused(focused bool) {
	c.focused = focused
	c.input.Focused = focused
}

// HandleInput handles keyboard input.
func (c *ExtensionInputComponent) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) || data == "\n" {
		c.onSubmit(c.input.GetValue())
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		c.onCancel()
	} else {
		c.input.HandleInput(data)
	}
}

// Dispose stops the countdown timer if active.
func (c *ExtensionInputComponent) Dispose() {
	if c.countdown != nil {
		c.countdown.Dispose()
	}
}
