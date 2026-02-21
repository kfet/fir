// Ported from: packages/coding-agent/src/modes/interactive/components/skill-invocation-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// SkillInvocationMessageComponent renders a skill invocation message with collapsed/expanded state.
// Only renders the skill block itself — user message is rendered separately.
type SkillInvocationMessageComponent struct {
	*tuicomp.Box
	expanded    bool
	skillBlock  *core.ParsedSkillBlock
	markdownThm tuicomp.MarkdownTheme
}

// NewSkillInvocationMessageComponent creates a new SkillInvocationMessageComponent.
func NewSkillInvocationMessageComponent(skillBlock *core.ParsedSkillBlock, mdTheme *tuicomp.MarkdownTheme) *SkillInvocationMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}
	t := theme.GetTheme()
	s := &SkillInvocationMessageComponent{
		Box:         tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		skillBlock:  skillBlock,
		markdownThm: *mdTheme,
	}
	s.updateDisplay()
	return s
}

// SetExpanded sets the expanded state and rebuilds the display.
func (s *SkillInvocationMessageComponent) SetExpanded(expanded bool) {
	s.expanded = expanded
	s.updateDisplay()
}

// Invalidate rebuilds the display.
func (s *SkillInvocationMessageComponent) Invalidate() {
	s.Box.Invalidate()
	s.updateDisplay()
}

func (s *SkillInvocationMessageComponent) updateDisplay() {
	s.Clear()
	t := theme.GetTheme()

	if s.expanded {
		// Expanded: label + skill name header + full content
		label := t.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m")
		s.AddChild(tuicomp.NewText(label, 0, 0, nil))
		header := "**" + s.skillBlock.Name + "**\n\n"
		s.AddChild(tuicomp.NewMarkdown(header+s.skillBlock.Content, 0, 0, s.markdownThm, &tuicomp.DefaultTextStyle{
			Color: func(str string) string { return t.Fg("customMessageText", str) },
		}))
	} else {
		// Collapsed: single line - [skill] name (hint to expand)
		line := t.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m ") +
			t.Fg("customMessageText", s.skillBlock.Name) +
			t.Fg("dim", " ("+EditorKey(tuicomp.ActExpandTools)+" to expand)")
		s.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}
