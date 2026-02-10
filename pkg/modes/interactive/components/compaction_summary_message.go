// Ported from: packages/coding-agent/src/modes/interactive/components/compaction-summary-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"

	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// CompactionSummaryMessageComponent renders a compaction summary with collapsed/expanded state.
type CompactionSummaryMessageComponent struct {
	*tuicomp.Box
	expanded    bool
	message     *core.CompactionSummaryMessage
	markdownThm tuicomp.MarkdownTheme
}

// NewCompactionSummaryMessageComponent creates a new CompactionSummaryMessageComponent.
func NewCompactionSummaryMessageComponent(message *core.CompactionSummaryMessage, mdTheme *tuicomp.MarkdownTheme) *CompactionSummaryMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}
	t := theme.GetTheme()
	c := &CompactionSummaryMessageComponent{
		Box:         tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		message:     message,
		markdownThm: *mdTheme,
	}
	c.updateDisplay()
	return c
}

// SetExpanded sets the expanded state and rebuilds the display.
func (c *CompactionSummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// Invalidate rebuilds the display.
func (c *CompactionSummaryMessageComponent) Invalidate() {
	c.Box.Invalidate()
	c.updateDisplay()
}

// formatTokenCount formats a token count with locale-style commas.
func formatTokenCount(n int) string {
	if n < 0 {
		return fmt.Sprintf("-%s", formatTokenCount(-n))
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func (c *CompactionSummaryMessageComponent) updateDisplay() {
	c.Clear()
	t := theme.GetTheme()

	tokenStr := formatTokenCount(c.message.TokensBefore)
	label := t.Fg("customMessageLabel", "\x1b[1m[compaction]\x1b[22m")
	c.AddChild(tuicomp.NewText(label, 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	if c.expanded {
		header := fmt.Sprintf("**Compacted from %s tokens**\n\n", tokenStr)
		c.AddChild(tuicomp.NewMarkdown(header+c.message.Summary, 0, 0, c.markdownThm, &tuicomp.DefaultTextStyle{
			Color: func(s string) string { return t.Fg("customMessageText", s) },
		}))
	} else {
		line := t.Fg("customMessageText", fmt.Sprintf("Compacted from %s tokens (", tokenStr)) +
			t.Fg("dim", EditorKey(tuicomp.ActExpandTools)) +
			t.Fg("customMessageText", " to expand)")
		c.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}
