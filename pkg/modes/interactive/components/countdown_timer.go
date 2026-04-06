// Ported from: packages/coding-agent/src/modes/interactive/components/countdown-timer.ts
// Upstream hash: 1caadb2e
package components

import (
	"math"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/tui"
)

// CountdownTimer is a reusable countdown timer for dialog components.
type CountdownTimer struct {
	mu               sync.Mutex
	remainingSeconds int
	tuiRef           *tui.TUI
	onTick           func(seconds int)
	onExpire         func()
	ticker           *time.Ticker
	done             chan struct{}
	stopped          bool
}

// countdownTickInterval is the tick interval for countdown timers.
// Tests may override this for speed.
var countdownTickInterval = time.Second

// NewCountdownTimer creates and starts a countdown timer.
func NewCountdownTimer(timeoutMs int, tuiRef *tui.TUI, onTick func(int), onExpire func()) *CountdownTimer {
	ct := &CountdownTimer{
		remainingSeconds: int(math.Ceil(float64(timeoutMs) / 1000.0)),
		tuiRef:           tuiRef,
		onTick:           onTick,
		onExpire:         onExpire,
		done:             make(chan struct{}),
	}

	// Initial tick
	ct.onTick(ct.remainingSeconds)

	ct.ticker = time.NewTicker(countdownTickInterval)
	go ct.run()
	return ct
}

func (ct *CountdownTimer) run() {
	for {
		select {
		case <-ct.ticker.C:
			ct.mu.Lock()
			if ct.stopped {
				ct.mu.Unlock()
				return
			}
			ct.remainingSeconds--
			seconds := ct.remainingSeconds
			ct.mu.Unlock()

			ct.onTick(seconds)
			if ct.tuiRef != nil {
				ct.tuiRef.RequestRender(false)
			}

			if seconds <= 0 {
				ct.Dispose()
				ct.onExpire()
				return
			}
		case <-ct.done:
			return
		}
	}
}

// Dispose stops the countdown timer.
func (ct *CountdownTimer) Dispose() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.stopped {
		return
	}
	ct.stopped = true
	ct.ticker.Stop()
	close(ct.done)
}
