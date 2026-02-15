// Ported from: packages/coding-agent/src/modes/interactive/components/user-message-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"strings"

	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// UserMessageItem represents a selectable user message for branching.
type UserMessageItem struct {
	ID        string // Entry ID in the session
	Text      string // The message text
	Timestamp string // Optional timestamp
}

// UserMessageList is a custom list component for user message selection.
type UserMessageList struct {
	messages      []UserMessageItem
	selectedIndex int
	maxVisible    int
	OnSelect      func(entryID string)
	OnCancel      func()
}

var _ tui.Component = (*UserMessageList)(nil)
var _ tui.InputHandler = (*UserMessageList)(nil)

// NewUserMessageList creates a new UserMessageList.
func NewUserMessageList(messages []UserMessageItem) *UserMessageList {
	sel := len(messages) - 1
	if sel < 0 {
		sel = 0
	}
	return &UserMessageList{
		messages:      messages,
		selectedIndex: sel,
		maxVisible:    10,
	}
}

// Invalidate is a no-op.
func (l *UserMessageList) Invalidate() {}

// Render renders the message list.
func (l *UserMessageList) Render(width int) []string {
	t := theme.GetTheme()
	var lines []string

	if len(l.messages) == 0 {
		lines = append(lines, t.Fg("muted", "  No user messages found"))
		return lines
	}

	// Calculate visible range with scrolling
	startIndex := l.selectedIndex - l.maxVisible/2
	if startIndex > len(l.messages)-l.maxVisible {
		startIndex = len(l.messages) - l.maxVisible
	}
	if startIndex < 0 {
		startIndex = 0
	}
	endIndex := startIndex + l.maxVisible
	if endIndex > len(l.messages) {
		endIndex = len(l.messages)
	}

	for i := startIndex; i < endIndex; i++ {
		msg := l.messages[i]
		isSelected := i == l.selectedIndex

		// Normalize to single line
		normalized := strings.ReplaceAll(strings.TrimSpace(msg.Text), "\n", " ")

		// First line: cursor + message
		cursor := "  "
		if isSelected {
			cursor = t.Fg("accent", "› ")
		}
		maxMsgWidth := width - 2
		truncated := tui.TruncateToWidth(normalized, maxMsgWidth, "…", false)
		if isSelected {
			truncated = t.Bold(truncated)
		}
		lines = append(lines, cursor+truncated)

		// Second line: metadata
		position := i + 1
		metadata := fmt.Sprintf("  Message %d of %d", position, len(l.messages))
		lines = append(lines, t.Fg("muted", metadata))
		lines = append(lines, "") // blank line between
	}

	// Scroll indicator
	if startIndex > 0 || endIndex < len(l.messages) {
		scrollInfo := fmt.Sprintf("  (%d/%d)", l.selectedIndex+1, len(l.messages))
		lines = append(lines, t.Fg("muted", scrollInfo))
	}

	return lines
}

// HandleInput handles keyboard input for navigation and selection.
func (l *UserMessageList) HandleInput(data string) {
	if len(l.messages) == 0 {
		if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) && l.OnCancel != nil {
			l.OnCancel()
		}
		return
	}

	switch {
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp):
		if l.selectedIndex == 0 {
			l.selectedIndex = len(l.messages) - 1
		} else {
			l.selectedIndex--
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown):
		if l.selectedIndex == len(l.messages)-1 {
			l.selectedIndex = 0
		} else {
			l.selectedIndex++
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm):
		if l.selectedIndex >= 0 && l.selectedIndex < len(l.messages) && l.OnSelect != nil {
			l.OnSelect(l.messages[l.selectedIndex].ID)
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel):
		if l.OnCancel != nil {
			l.OnCancel()
		}
	}
}

// UserMessageSelectorComponent renders a user message selector for branching.
type UserMessageSelectorComponent struct {
	tui.Container
	messageList *UserMessageList
}

var _ tui.Component = (*UserMessageSelectorComponent)(nil)

// NewUserMessageSelectorComponent creates a new UserMessageSelectorComponent.
func NewUserMessageSelectorComponent(messages []UserMessageItem, onSelect func(entryID string), onCancel func()) *UserMessageSelectorComponent {
	t := theme.GetTheme()

	c := &UserMessageSelectorComponent{}

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(tuicomp.NewText(t.Bold("Branch from Message"), 1, 0, nil))
	c.AddChild(tuicomp.NewText(t.Fg("muted", "Select a message to create a new branch from that point"), 1, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.messageList = NewUserMessageList(messages)
	c.messageList.OnSelect = onSelect
	c.messageList.OnCancel = onCancel
	c.AddChild(c.messageList)

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// GetMessageList returns the underlying message list for input handling.
func (c *UserMessageSelectorComponent) GetMessageList() *UserMessageList {
	return c.messageList
}
