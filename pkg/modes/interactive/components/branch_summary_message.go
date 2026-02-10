// Ported from: packages/coding-agent/src/modes/interactive/components/branch-summary-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// BranchSummaryMessageComponent renders a branch summary with collapsed/expanded state.
type BranchSummaryMessageComponent struct {
	*tuicomp.Box
	expanded    bool
	message     *core.BranchSummaryMessage
	markdownThm tuicomp.MarkdownTheme
}

// NewBranchSummaryMessageComponent creates a new BranchSummaryMessageComponent.
func NewBranchSummaryMessageComponent(message *core.BranchSummaryMessage, mdTheme *tuicomp.MarkdownTheme) *BranchSummaryMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}
	t := theme.GetTheme()
	b := &BranchSummaryMessageComponent{
		Box:         tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		message:     message,
		markdownThm: *mdTheme,
	}
	b.updateDisplay()
	return b
}

// SetExpanded sets the expanded state and rebuilds the display.
func (b *BranchSummaryMessageComponent) SetExpanded(expanded bool) {
	b.expanded = expanded
	b.updateDisplay()
}

// Invalidate rebuilds the display.
func (b *BranchSummaryMessageComponent) Invalidate() {
	b.Box.Invalidate()
	b.updateDisplay()
}

func (b *BranchSummaryMessageComponent) updateDisplay() {
	b.Clear()
	t := theme.GetTheme()

	label := t.Fg("customMessageLabel", "\x1b[1m[branch]\x1b[22m")
	b.AddChild(tuicomp.NewText(label, 0, 0, nil))
	b.AddChild(tuicomp.NewSpacer(1))

	if b.expanded {
		header := "**Branch Summary**\n\n"
		b.AddChild(tuicomp.NewMarkdown(header+b.message.Summary, 0, 0, b.markdownThm, &tuicomp.DefaultTextStyle{
			Color: func(s string) string { return t.Fg("customMessageText", s) },
		}))
	} else {
		line := t.Fg("customMessageText", "Branch summary (") +
			t.Fg("dim", EditorKey(tuicomp.ActExpandTools)) +
			t.Fg("customMessageText", " to expand)")
		b.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}
