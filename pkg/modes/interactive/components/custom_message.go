// Ported from: packages/coding-agent/src/modes/interactive/components/custom-message.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	"github.com/kfet/fir/pkg/tui/components"
)

// MessageRenderer is a function that can render a custom message.
// Returns nil if it cannot handle the message, letting the default renderer take over.
type MessageRenderer func(msg *core.CustomMessage, expanded bool, theme *theme.Theme) tui.Component

// CustomMessageComponent renders a custom message entry from extensions.
// Uses distinct styling to differentiate from user messages.
type CustomMessageComponent struct {
	tui.Container
	message         *core.CustomMessage
	customRenderer  MessageRenderer
	box             *components.Box
	customComponent tui.Component
	markdownTheme   components.MarkdownTheme
	expanded        bool
}

var _ tui.Component = (*CustomMessageComponent)(nil)

// NewCustomMessageComponent creates a new CustomMessageComponent.
// customRenderer is optional and will be tried first for rendering.
// If mdTheme is nil, it uses the global theme's markdown theme.
func NewCustomMessageComponent(
	message *core.CustomMessage,
	customRenderer MessageRenderer,
	mdTheme *components.MarkdownTheme,
) *CustomMessageComponent {
	if mdTheme == nil {
		mt := theme.GetMarkdownTheme()
		mdTheme = &mt
	}

	t := theme.GetTheme()

	c := &CustomMessageComponent{
		message:        message,
		customRenderer: customRenderer,
		box:            components.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		markdownTheme:  *mdTheme,
	}

	c.AddChild(components.NewSpacer(1))
	c.rebuild()

	return c
}

// SetExpanded sets the expanded state and rebuilds the display.
func (c *CustomMessageComponent) SetExpanded(expanded bool) {
	if c.expanded != expanded {
		c.expanded = expanded
		c.rebuild()
	}
}

// Invalidate invalidates and rebuilds.
func (c *CustomMessageComponent) Invalidate() {
	c.Container.Invalidate()
	c.rebuild()
}

func (c *CustomMessageComponent) rebuild() {
	// Remove previous custom component
	if c.customComponent != nil {
		c.RemoveChild(c.customComponent)
		c.customComponent = nil
	}
	c.RemoveChild(c.box)

	// Try custom renderer first
	if c.customRenderer != nil {
		t := theme.GetTheme()
		comp := c.customRenderer(c.message, c.expanded, t)
		if comp != nil {
			c.customComponent = comp
			c.AddChild(comp)
			return
		}
	}

	// Default rendering uses the box
	c.AddChild(c.box)
	c.box.Clear()

	t := theme.GetTheme()

	// Label
	label := t.Fg("customMessageLabel", "\x1b[1m["+c.message.CustomType+"]\x1b[22m")
	c.box.AddChild(components.NewText(label, 0, 0, nil))
	c.box.AddChild(components.NewSpacer(1))

	// Extract text content
	text := extractCustomMessageText(c.message)

	c.box.AddChild(components.NewMarkdown(text, 0, 0, c.markdownTheme, &components.DefaultTextStyle{
		Color: func(s string) string { return t.Fg("customMessageText", s) },
	}))
}

// extractCustomMessageText extracts the text content from a custom message.
func extractCustomMessageText(msg *core.CustomMessage) string {
	switch content := msg.Content.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, item := range content {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
