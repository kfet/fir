// Ported from: packages/tui/src/components/loader.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/tui"
)

// elapsedThreshold is how long a loader can spin before we start appending
// a compact elapsed counter to the message. This is a liveness/duration cue
// that complements the ASCII spinner glyph — the spinner proves the goroutine
// is alive, the counter shows how long we've been waiting.
const elapsedThreshold = 30 * time.Second

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
	startedAt      time.Time
	nowFn          func() time.Time // overrideable for tests
}

var _ tui.Component = (*Loader)(nil)

// NewLoader creates and starts a loader with the given spinner and message color functions.
func NewLoader(ui RenderRequester, spinnerColorFn, messageColorFn func(string) string, message string) *Loader {
	l := &Loader{
		text:           NewText("", 1, 0, nil),
		frames:         []string{"|", "/", "-", "\\"},
		ui:             ui,
		spinnerColorFn: spinnerColorFn,
		messageColorFn: messageColorFn,
		message:        message,
		done:           make(chan struct{}),
		nowFn:          time.Now,
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
	l.startedAt = l.nowFn()
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

// SetMessage updates the loader message. Also resets the elapsed counter,
// since a message change means the work has progressed.
func (l *Loader) SetMessage(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.message = message
	l.startedAt = l.nowFn()
	l.updateDisplay()
	if l.ui != nil {
		l.ui.RequestRender()
	}
}

// SetClock overrides the clock used for elapsed-time tracking. For tests.
func (l *Loader) SetClock(nowFn func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nowFn = nowFn
}

// StartedAt returns when the loader's elapsed counter was last reset
// (creation or last SetMessage). For tests.
func (l *Loader) StartedAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.startedAt
}

// formatElapsed renders a duration as a compact tmux-style counter
// (10s, 1m05s, 1h05m). Mirrors the tmuxspinner extension format.
func formatElapsed(d time.Duration) string {
	s := int(d / time.Second)
	if s < 0 {
		s = 0
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// updateDisplay must be called with l.mu held.
func (l *Loader) updateDisplay() {
	frame := l.frames[l.currentFrame]
	msg := l.message
	if !l.startedAt.IsZero() {
		elapsed := l.nowFn().Sub(l.startedAt)
		if elapsed >= elapsedThreshold {
			msg = msg + " " + formatElapsed(elapsed)
		}
	}
	l.text.SetText(l.spinnerColorFn(frame) + " " + l.messageColorFn(msg))
}
