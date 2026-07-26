// Ported from: packages/coding-agent/src/modes/interactive/components/extension-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

// ExtensionSelectorOptions configures an ExtensionSelectorComponent.
type ExtensionSelectorOptions struct {
	TUI     *tui.TUI
	Timeout int // timeout in milliseconds, 0 = no timeout
}

// ExtensionSelectorComponent is a generic selector for extensions.
// Displays a list of string options with keyboard navigation.
type ExtensionSelectorComponent struct {
	tui.Container
	options       []string
	selectedIndex int
	listContainer *tui.Container
	onSelect      func(option string)
	onCancel      func()
	titleText     *tuicomp.Text
	baseTitle     string
	countdown     *CountdownTimer
}

var _ tui.Component = (*ExtensionSelectorComponent)(nil)
var _ tui.InputHandler = (*ExtensionSelectorComponent)(nil)

// NewExtensionSelectorComponent creates a new ExtensionSelectorComponent.
func NewExtensionSelectorComponent(
	title string,
	options []string,
	onSelect func(option string),
	onCancel func(),
	opts *ExtensionSelectorOptions,
) *ExtensionSelectorComponent {
	t := theme.GetTheme()

	c := &ExtensionSelectorComponent{
		options:       options,
		onSelect:      onSelect,
		onCancel:      onCancel,
		baseTitle:     title,
		listContainer: &tui.Container{},
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

	c.AddChild(c.listContainer)
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(tuicomp.NewText(
		RawKeyHint("↑↓", "navigate")+"  "+KeyHint("selectConfirm", "select")+"  "+KeyHint("selectCancel", "cancel"),
		1, 0, nil,
	))
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	c.updateList()

	return c
}

func (c *ExtensionSelectorComponent) updateList() {
	t := theme.GetTheme()
	c.listContainer.Clear()

	for i, opt := range c.options {
		var text string
		if i == c.selectedIndex {
			text = t.Fg("accent", "→ ") + t.Fg("accent", opt)
		} else {
			text = "  " + t.Fg("text", opt)
		}
		c.listContainer.AddChild(tuicomp.NewText(text, 1, 0, nil))
	}
}

// HandleInput handles keyboard input for navigation and selection.
func (c *ExtensionSelectorComponent) HandleInput(data string) {
	switch {
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) || data == "k":
		if c.selectedIndex > 0 {
			c.selectedIndex--
			c.updateList()
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) || data == "j":
		if c.selectedIndex < len(c.options)-1 {
			c.selectedIndex++
			c.updateList()
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) || data == "\n":
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.options) {
			c.onSelect(c.options[c.selectedIndex])
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel):
		c.onCancel()
	}
}

// Dispose stops the countdown timer if active.
func (c *ExtensionSelectorComponent) Dispose() {
	if c.countdown != nil {
		c.countdown.Dispose()
	}
}
