// Ported from: packages/coding-agent/src/modes/interactive/components/assistant-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// AssistantMessageComponent renders a complete assistant message.
type AssistantMessageComponent struct {
	tui.Container
	contentContainer *tui.Container
	hideThinking     bool
	markdownThm      tuicomp.MarkdownTheme
	lastMessage      *ai.AssistantMessage
}

// NewAssistantMessageComponent creates a new AssistantMessageComponent.
// message may be nil for deferred content.
func NewAssistantMessageComponent(message *ai.AssistantMessage, hideThinking bool, mdTheme *tuicomp.MarkdownTheme) *AssistantMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}
	a := &AssistantMessageComponent{
		contentContainer: &tui.Container{},
		hideThinking:     hideThinking,
		markdownThm:      *mdTheme,
	}
	a.AddChild(a.contentContainer)
	if message != nil {
		a.UpdateContent(message)
	}
	return a
}

// Invalidate rebuilds using the last message.
func (a *AssistantMessageComponent) Invalidate() {
	a.markdownThm = theme.GetMarkdownTheme()
	a.Container.Invalidate()
	if a.lastMessage != nil {
		a.UpdateContent(a.lastMessage)
	}
}

// SetHideThinkingBlock sets whether thinking blocks are hidden.
func (a *AssistantMessageComponent) SetHideThinkingBlock(hide bool) {
	a.hideThinking = hide
}

// UpdateContent updates the rendered content from an assistant message.
func (a *AssistantMessageComponent) UpdateContent(message *ai.AssistantMessage) {
	if message == nil {
		return
	}
	a.lastMessage = message
	a.contentContainer.Clear()

	t := theme.GetTheme()

	// hasVisibleBody reports whether a content block carries any
	// non-whitespace renderable body — text, thinking text, or a
	// server-content display string.
	hasVisibleBody := func(c ai.AssistantContent) bool {
		if c.Text != nil && strings.TrimSpace(c.Text.Text) != "" {
			return true
		}
		if c.Thinking != nil && strings.TrimSpace(c.Thinking.Thinking) != "" {
			return true
		}
		if c.Server != nil && strings.TrimSpace(c.Server.Display) != "" {
			return true
		}
		return false
	}

	hasVisibleContent := false
	for _, c := range message.Content {
		if hasVisibleBody(c) {
			hasVisibleContent = true
			break
		}
	}

	if hasVisibleContent {
		a.contentContainer.AddChild(tuicomp.NewSpacer(1))
	}

	for i, content := range message.Content {
		if content.Text != nil && strings.TrimSpace(content.Text.Text) != "" {
			a.contentContainer.AddChild(tuicomp.NewMarkdown(strings.TrimSpace(content.Text.Text), 1, 0, a.markdownThm, nil))
		} else if content.Server != nil && strings.TrimSpace(content.Server.Display) != "" {
			a.contentContainer.AddChild(tuicomp.NewMarkdown(strings.TrimSpace(content.Server.Display), 1, 0, a.markdownThm, nil))
		} else if content.Thinking != nil && strings.TrimSpace(content.Thinking.Thinking) != "" {
			// Check if another visible content block follows
			hasVisibleAfter := false
			for _, c := range message.Content[i+1:] {
				if hasVisibleBody(c) {
					hasVisibleAfter = true
					break
				}
			}

			if a.hideThinking {
				a.contentContainer.AddChild(tuicomp.NewText(
					t.Italic(t.Fg("thinkingText", "Thinking...")), 1, 0, nil))
				if hasVisibleAfter {
					a.contentContainer.AddChild(tuicomp.NewSpacer(1))
				}
			} else {
				border := tuicomp.NewRoundedBorder(
					func(s string) string { return t.Fg("borderMuted", s) },
					1, // inner horizontal padding
				)
				border.AddChild(tuicomp.NewMarkdown(
					strings.TrimSpace(content.Thinking.Thinking), 0, 0, a.markdownThm, nil))
				a.contentContainer.AddChild(tuicomp.NewIndented(border, 1))
				if hasVisibleAfter {
					a.contentContainer.AddChild(tuicomp.NewSpacer(1))
				}
			}
		}
	}

	// Handle abort/error when no tool calls are present
	hasToolCalls := false
	for _, c := range message.Content {
		if c.ToolCall != nil {
			hasToolCalls = true
			break
		}
	}

	if !hasToolCalls {
		if message.StopReason == ai.StopReasonAborted {
			abortMsg := "Operation aborted"
			if message.ErrorMessage != "" && message.ErrorMessage != "Request was aborted" {
				abortMsg = message.ErrorMessage
			}
			a.contentContainer.AddChild(tuicomp.NewSpacer(1))
			a.contentContainer.AddChild(tuicomp.NewText(t.MutedTimestamp(time.Now())+t.Fg("error", abortMsg), 1, 0, nil))
		} else if message.StopReason == ai.StopReasonError {
			errorMsg := message.ErrorMessage
			if errorMsg == "" {
				errorMsg = "Unknown error"
			}
			a.contentContainer.AddChild(tuicomp.NewSpacer(1))
			a.contentContainer.AddChild(tuicomp.NewText(t.MutedTimestamp(time.Now())+t.Fg("error", "Error: "+errorMsg), 1, 0, nil))
		}
	}
}
