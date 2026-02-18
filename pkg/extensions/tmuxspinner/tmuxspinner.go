// Package tmuxspinner adds a spinner to the tmux window name while the agent
// is working. When the agent is idle the original window name is restored.
//
// The extension uses `tmux rename-window` to set the window name, which works
// reliably regardless of tmux's allow-rename setting.
//
// The extension is a no-op when not running inside tmux ($TMUX unset).
//
// Import this package to enable the extension:
//
//	import _ "github.com/kfet/tau/pkg/extensions/tmuxspinner"
package tmuxspinner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kfet/tau/pkg/extension"
)

// Spinner frames (braille dots).
var frames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// spinInterval controls how frequently the spinner frame advances.
const spinInterval = 150 * time.Millisecond

// isTmux reports whether the process is inside tmux. Replaceable for tests.
var isTmux = func() bool {
	return os.Getenv("TMUX") != ""
}

// readPaneID returns the tmux pane ID (e.g. "%0") for the process. This is
// set by tmux in $TMUX_PANE for every spawned process and is stable
// regardless of which window is currently active. tmux accepts pane IDs as
// -t targets for rename-window and set-window-option commands.
// Replaceable for tests.
var readPaneID = func() string {
	return os.Getenv("TMUX_PANE")
}

// readWindowName reads the tmux window name for the given target.
// Replaceable for tests.
var readWindowName = func(target string) string {
	out, err := exec.Command("tmux", "display-message", "-t", target, "-p", "#W").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// renameWindow sets the tmux window name for the given target via
// `tmux rename-window -t`. Replaceable for tests.
var renameWindow = func(target, name string) {
	_ = exec.Command("tmux", "rename-window", "-t", target, name).Run()
}

// disableAutoRename turns off tmux's automatic-rename for the given target
// window so our explicit name changes stick. Replaceable for tests.
var disableAutoRename = func(target string) {
	_ = exec.Command("tmux", "set-window-option", "-t", target, "automatic-rename", "off").Run()
}

func init() {
	extension.Register("tmuxspinner", factory)
}

func factory(api extension.API) {
	if !isTmux() {
		return
	}

	s := &spinner{}

	api.On("agent_start", func(_ *extension.Event, _ extension.Context) (any, error) {
		s.Start()
		return nil, nil
	})

	api.On("agent_end", func(_ *extension.Event, _ extension.Context) (any, error) {
		s.Stop()
		return nil, nil
	})

	api.On("session_shutdown", func(_ *extension.Event, _ extension.Context) (any, error) {
		s.Stop()
		return nil, nil
	})
}

// spinner manages the background goroutine that cycles the tmux window name.
type spinner struct {
	mu       sync.Mutex
	paneID   string // tmux pane target (e.g. "%0") from $TMUX_PANE
	baseName string
	cancel   context.CancelFunc
	done     chan struct{} // closed when loop exits
	running  bool
}

// Start begins the spinner animation. Safe to call multiple times; subsequent
// calls are no-ops while the spinner is already running.
func (s *spinner) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	// Capture pane ID and window name once on first start
	if s.paneID == "" {
		s.paneID = readPaneID()
		if s.paneID == "" {
			// Cannot target a specific window; bail out.
			return
		}
		s.baseName = readWindowName(s.paneID)
		if s.baseName == "" {
			s.baseName = "tau"
		}
		disableAutoRename(s.paneID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.running = true

	go s.loop(ctx, done)
}

// Stop stops the spinner and restores the original window name.
// Safe to call when already stopped.
func (s *spinner) Stop() {
	s.mu.Lock()

	if !s.running {
		s.mu.Unlock()
		return
	}

	s.cancel()
	done := s.done
	s.running = false
	s.mu.Unlock()

	// Wait for the loop goroutine to exit before restoring name,
	// ensuring no rename calls happen after this point.
	<-done

	s.mu.Lock()
	target := s.paneID
	base := s.baseName
	s.mu.Unlock()

	renameWindow(target, base)
}

func (s *spinner) loop(ctx context.Context, done chan struct{}) {
	defer close(done)

	ticker := time.NewTicker(spinInterval)
	defer ticker.Stop()

	i := 0
	// lastSet tracks the window name we most recently wrote. On each tick we
	// read the actual tmux window name and compare: if it differs from lastSet
	// the user renamed the tab, so we adopt the new name as our base.
	lastSet := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			target := s.paneID
			s.mu.Unlock()

			// Detect user-initiated renames between ticks.
			if lastSet != "" {
				current := readWindowName(target)
				if current != lastSet && current != "" {
					s.mu.Lock()
					s.baseName = current
					s.mu.Unlock()
				}
			}

			s.mu.Lock()
			base := s.baseName
			s.mu.Unlock()

			name := fmt.Sprintf("%s %c", base, frames[i%len(frames)])
			renameWindow(target, name)
			lastSet = name
			i++
		}
	}
}
