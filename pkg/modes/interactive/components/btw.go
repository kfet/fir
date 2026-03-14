package components

import (
	"sync"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// BtwMessageComponent displays a /btw side-question and its streaming response.
// The exchange is ephemeral — it is never saved to session history.
type BtwMessageComponent struct {
	*tuicomp.Box
	mu       sync.Mutex
	question string
	response string
	done     bool
	failed   bool
	ui       *tui.TUI
}

// NewBtwMessageComponent creates the component and renders the initial state.
func NewBtwMessageComponent(question string, ui *tui.TUI) *BtwMessageComponent {
	t := theme.GetTheme()
	b := &BtwMessageComponent{
		Box:      tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		question: question,
		ui:       ui,
	}
	b.render()
	return b
}

// AppendChunk adds a text delta and requests a re-render.
func (b *BtwMessageComponent) AppendChunk(delta string) {
	b.mu.Lock()
	b.response += delta
	b.mu.Unlock()
	b.render()
	if b.ui != nil {
		b.ui.RequestRender(false)
	}
}

// SetDone marks the response as complete (or failed) and re-renders.
func (b *BtwMessageComponent) SetDone(failed bool) {
	b.mu.Lock()
	b.done = true
	b.failed = failed
	b.mu.Unlock()
	b.render()
	if b.ui != nil {
		b.ui.RequestRender(false)
	}
}

func (b *BtwMessageComponent) render() {
	b.mu.Lock()
	question := b.question
	response := b.response
	done := b.done
	failed := b.failed
	b.mu.Unlock()

	b.Clear()
	t := theme.GetTheme()

	// Header line: "[ btw ] question"
	label := t.Fg("customMessageLabel", "\x1b[1m[btw]\x1b[22m")
	header := label + " " + t.Fg("customMessageText", question)
	b.AddChild(tuicomp.NewText(header, 0, 0, nil))

	// Response body (streams in incrementally).
	if response != "" {
		b.AddChild(tuicomp.NewText(t.Fg("muted", response), 0, 0, nil))
	}

	// Footer once complete.
	if done {
		if failed {
			b.AddChild(tuicomp.NewText(t.Fg("warning", "(btw failed)"), 0, 0, nil))
		} else if response == "" {
			b.AddChild(tuicomp.NewText(t.Fg("dim", "(no response)"), 0, 0, nil))
		}
	}
}
