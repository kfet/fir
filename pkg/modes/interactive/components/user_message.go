// Ported from: packages/coding-agent/src/modes/interactive/components/user-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

// UserMessageComponent renders a user message with markdown formatting.
type UserMessageComponent struct {
	tui.Container
	text    string
	mdTheme tuicomp.MarkdownTheme
}

// NewUserMessageComponent creates a UserMessageComponent.
// If mdTheme is nil, uses the global markdown theme.
func NewUserMessageComponent(text string, mdTheme *tuicomp.MarkdownTheme) *UserMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}
	u := &UserMessageComponent{
		text:    text,
		mdTheme: *mdTheme,
	}
	u.rebuild()
	return u
}

func (u *UserMessageComponent) rebuild() {
	u.Clear()
	t := theme.GetTheme()
	u.AddChild(tuicomp.NewSpacer(1))
	u.AddChild(tuicomp.NewMarkdown(u.text, 1, 1, u.mdTheme, &tuicomp.DefaultTextStyle{
		BgColor: func(s string) string { return t.Bg("userMessageBg", s) },
		Color:   func(s string) string { return t.Fg("userMessageText", s) },
	}))
}
