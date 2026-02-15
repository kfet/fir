// Ported from: packages/tui/src/components/cancellable-loader.ts
// Upstream hash: 1caadb2e
package components

import (
	"sync"

	"github.com/kfet/tau/pkg/tui"
)

// CancellableLoader is a Loader that can be cancelled with Escape.
// It wraps a Loader and adds abort signaling.
type CancellableLoader struct {
	*Loader
	mu       sync.Mutex
	aborted  bool
	OnAbort  func()
	abortCh  chan struct{}
}

var _ tui.Component = (*CancellableLoader)(nil)

// NewCancellableLoader creates and starts a cancellable loader.
func NewCancellableLoader(ui RenderRequester, spinnerColorFn, messageColorFn func(string) string, message string) *CancellableLoader {
	return &CancellableLoader{
		Loader:  NewLoader(ui, spinnerColorFn, messageColorFn, message),
		abortCh: make(chan struct{}),
	}
}

// Aborted returns whether the loader was aborted.
func (c *CancellableLoader) Aborted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.aborted
}

// AbortCh returns a channel that is closed when the loader is aborted.
func (c *CancellableLoader) AbortCh() <-chan struct{} {
	return c.abortCh
}

// HandleInput handles keyboard input, aborting on selectCancel (escape/ctrl+c).
func (c *CancellableLoader) HandleInput(data string) {
	if MatchesEditorAction(data, ActSelectCancel) {
		c.mu.Lock()
		if c.aborted {
			c.mu.Unlock()
			return
		}
		c.aborted = true
		close(c.abortCh)
		onAbort := c.OnAbort
		c.mu.Unlock()

		if onAbort != nil {
			onAbort()
		}
	}
}

// Dispose stops the loader animation.
func (c *CancellableLoader) Dispose() {
	c.Loader.Stop()
}
