package interactive

import (
	"context"
	"fmt"
	"strings"
	"time"

	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/session"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// quickReplyTimeout bounds one quick reply.
const quickReplyTimeout = 60 * time.Second

// quickReply answers a message typed during a running turn from a branch
// of the session (session.Interject) and shows the answer. When the branch
// fails, or it chose to tell a turn that has since ended, the message is
// sent as a normal prompt (queued as a follow-up if a turn still runs).
func (m *InteractiveMode) quickReply(text string) {
	m.showStatus("Quick reply…")
	ctx, cancel := context.WithTimeout(context.Background(), quickReplyTimeout)
	r, err := m.session.Interject(ctx, text)
	cancel()
	m.commandStatusContainer.Clear()
	if err != nil {
		m.showWarning(fmt.Sprintf("Quick reply failed, sending as a message: %v", err))
	} else {
		m.showQuickReply(r.Text, r.Action.String())
		if r.Action != session.InterjectInform || r.Steered {
			return
		}
	}
	// The user message is already on screen: count it so the prompt's
	// message_start does not render it twice.
	m.pendingPreemptiveUserMsgs.Add(1)
	if err := m.session.Prompt(text); err != nil {
		m.showWarning(fmt.Sprintf("Failed to send message: %v", err))
	}
}

func (m *InteractiveMode) showQuickReply(text, tag string) {
	t := itheme.GetTheme()
	if text = strings.TrimSpace(text); text != "" {
		m.messageContainer.AddChild(tuicomp.NewMarkdown(text, 1, 0, m.markdownTheme,
			&tuicomp.DefaultTextStyle{Color: func(s string) string { return t.Fg("text", s) }}))
	}
	m.messageContainer.AddChild(tuicomp.NewText(t.Fg("muted", "(quick reply · "+tag+")"), 1, 0, nil))
	m.ui.RequestRender(false)
}
