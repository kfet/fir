// Ported from: packages/coding-agent/src/modes/interactive/components/oauth-selector.ts
// Upstream hash: 9a3b4c5d
package components

import (
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// OAuthProvider describes an OAuth provider for the selector.
type OAuthProvider struct {
	ID       string
	Name     string
	LoggedIn bool
}

// OAuthSelectorComponent renders an OAuth provider selector.
type OAuthSelectorComponent struct {
	tui.Container
	listContainer *tui.Container
	providers     []OAuthProvider
	selectedIndex int
	mode          string // "login" or "logout"
	onSelect      func(providerID string)
	onCancel      func()
}

// NewOAuthSelectorComponent creates a new OAuth selector.
func NewOAuthSelectorComponent(
	mode string,
	providers []OAuthProvider,
	onSelect func(providerID string),
	onCancel func(),
) *OAuthSelectorComponent {
	t := theme.GetTheme()
	c := &OAuthSelectorComponent{
		providers: providers,
		mode:      mode,
		onSelect:  onSelect,
		onCancel:  onCancel,
	}

	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	title := "Select provider to login:"
	if mode == "logout" {
		title = "Select provider to logout:"
	}
	c.AddChild(tuicomp.NewText(t.Bold(title), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.listContainer = &tui.Container{}
	c.AddChild(c.listContainer)
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	c.updateList()
	return c
}

func (c *OAuthSelectorComponent) updateList() {
	t := theme.GetTheme()
	c.listContainer.Clear()

	for i, p := range c.providers {
		isSelected := i == c.selectedIndex
		statusIndicator := ""
		if p.LoggedIn {
			statusIndicator = t.Fg("success", " ✓ logged in")
		}

		var line string
		if isSelected {
			line = t.Fg("accent", "→ ") + t.Fg("accent", p.Name) + statusIndicator
		} else {
			line = "  " + p.Name + statusIndicator
		}
		c.listContainer.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}

	if len(c.providers) == 0 {
		msg := "No OAuth providers available"
		if c.mode == "logout" {
			msg = "No OAuth providers logged in. Use /login first."
		}
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  "+msg), 0, 0, nil))
	}
}

// HandleInput processes keyboard input.
func (c *OAuthSelectorComponent) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		if c.selectedIndex > 0 {
			c.selectedIndex--
			c.updateList()
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		if c.selectedIndex < len(c.providers)-1 {
			c.selectedIndex++
			c.updateList()
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) {
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.providers) {
			c.onSelect(c.providers[c.selectedIndex].ID)
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		c.onCancel()
	}
}
