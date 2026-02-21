// Ported from: packages/tui/src/components/loader.ts
// Upstream hash: 1caadb2e
package components

import (
	"sync"
	"time"

	"github.com/kfet/fir/pkg/tui"
)

// RenderRequester is the interface for requesting a re-render.
type RenderRequester interface {
	RequestRender()
}

// Loader displays a spinning animation with a message.
type Loader struct {
	text           *Text
	frames         []string
	currentFrame   int
	mu             sync.Mutex
	ticker         *time.Ticker
	done           chan struct{}
	ui             RenderRequester
	spinnerColorFn func(string) string
	messageColorFn func(string) string
	message        string
}

var _ tui.Component = (*Loader)(nil)

// NewLoader creates and starts a loader with the given spinner and message color functions.
func NewLoader(ui RenderRequester, spinnerColorFn, messageColorFn func(string) string, message string) *Loader {
	l := &Loader{
		text:           NewText("", 1, 0, nil),
		frames:         []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		ui:             ui,
		spinnerColorFn: spinnerColorFn,
		messageColorFn: messageColorFn,
		message:        message,
		done:           make(chan struct{}),
	}
	l.Start()
	return l
}

// Render returns an empty line followed by the spinner+message text.
func (l *Loader) Render(width int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	textLines := l.text.Render(width)
	result := make([]string, 0, 1+len(textLines))
	result = append(result, "")
	result = append(result, textLines...)
	return result
}

// Invalidate clears cached render state.
func (l *Loader) Invalidate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text.Invalidate()
}

// Start begins the spinner animation.
func (l *Loader) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updateDisplay()
	l.ticker = time.NewTicker(80 * time.Millisecond)
	tickCh := l.ticker.C // capture channel before goroutine
	go func() {
		for {
			select {
			case <-l.done:
				return
			case <-tickCh:
				l.mu.Lock()
				l.currentFrame = (l.currentFrame + 1) % len(l.frames)
				l.updateDisplay()
				l.mu.Unlock()
				if l.ui != nil {
					l.ui.RequestRender()
				}
			}
		}
	}()
}

// Stop stops the spinner animation.
func (l *Loader) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ticker != nil {
		l.ticker.Stop()
		l.ticker = nil
	}
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

// SetMessage updates the loader message.
func (l *Loader) SetMessage(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.message = message
	l.updateDisplay()
	if l.ui != nil {
		l.ui.RequestRender()
	}
}

// updateDisplay must be called with l.mu held.
func (l *Loader) updateDisplay() {
	frame := l.frames[l.currentFrame]
	l.text.SetText(l.spinnerColorFn(frame) + " " + l.messageColorFn(l.message))
}
