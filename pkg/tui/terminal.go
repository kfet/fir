// Ported from: packages/tui/src/terminal.ts
// Upstream hash: 380236a0
package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Terminal is the minimal terminal interface for TUI.
type Terminal interface {
	// Start the terminal with input and resize handlers.
	Start(onInput func(data string), onResize func())

	// Stop the terminal and restore state.
	Stop()

	// DrainInput drains stdin before exiting to prevent leaked escape sequences.
	DrainInput(maxMs, idleMs int)

	// Write output to terminal.
	Write(data string)

	// Columns returns the terminal width.
	Columns() int

	// Rows returns the terminal height.
	Rows() int

	// MoveBy moves cursor up (negative) or down (positive) by N lines.
	MoveBy(lines int)

	// HideCursor hides the terminal cursor.
	HideCursor()

	// ShowCursor shows the terminal cursor.
	ShowCursor()

	// ClearLine clears the current line.
	ClearLine()

	// ClearFromCursor clears from cursor to end of screen.
	ClearFromCursor()

	// ClearScreen clears the entire screen and moves cursor to (0,0).
	ClearScreen()

	// SetTitle sets the terminal window title.
	SetTitle(title string)
}

// ProcessTerminal is a real terminal using os.Stdin/os.Stdout.
type ProcessTerminal struct {
	mu             sync.Mutex
	oldState       *term.State
	inputHandler   func(data string)
	resizeHandler  func()
	sigwinchCh     chan os.Signal
	stopCh         chan struct{}
	stopped        bool
}

// isKittyCompatibleTerminal reports whether the current terminal natively
// supports the Kitty keyboard protocol (i.e. will honour \x1b[>1u and send
// unambiguous CSI-u sequences such as \x1b[13;2u for Shift+Enter).
//
// We use well-known environment variables rather than a synchronous
// terminal query to avoid complicating startup with a read timeout.
func isKittyCompatibleTerminal() bool {
	// Kitty terminal itself
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return true
	}
	if strings.ToLower(os.Getenv("TERM")) == "xterm-kitty" {
		return true
	}
	// Detect Kitty-compatible terminals through tmux/screen.
	// When running inside tmux, TERM_PROGRAM is "tmux" but the outer
	// terminal's env vars are still available.
	if os.Getenv("GHOSTTY_BIN_DIR") != "" {
		return true
	}
	if os.Getenv("WEZTERM_EXECUTABLE") != "" {
		return true
	}
	return false
}

// NewProcessTerminal creates a new ProcessTerminal.
func NewProcessTerminal() *ProcessTerminal {
	return &ProcessTerminal{}
}

// Start enables raw mode and sets up input/resize handlers.
func (t *ProcessTerminal) Start(onInput func(data string), onResize func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.inputHandler = onInput
	t.resizeHandler = onResize
	t.stopCh = make(chan struct{})
	t.stopped = false

	// Enable raw mode
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err == nil {
		t.oldState = oldState
	}

	// On Windows, enable ENABLE_VIRTUAL_TERMINAL_INPUT so the console sends
	// VT escape sequences (e.g. \x1b[Z for Shift+Tab) instead of raw console
	// events that lose modifier information. Must run AFTER MakeRaw since
	// that resets console mode flags.
	enableWindowsVTInput()

	// Enable bracketed paste mode
	os.Stdout.WriteString("\x1b[?2004h")

	// Determine if we're inside tmux and whether the outer terminal supports
	// the Kitty keyboard protocol.
	inTmux := os.Getenv("TMUX") != ""
	kittyCompat := isKittyCompatibleTerminal()

	// Enable modifyOtherKeys level 2 so that modifier+key combos (e.g.
	// Shift+Enter → \x1b[27;2;13~, Alt+Enter → \x1b[27;3;13~) are reported
	// as unambiguous escape sequences rather than the legacy \x1b\r which
	// collides with both alt+enter and shift+enter depending on the terminal.
	//
	// Skip this inside tmux when the outer terminal supports Kitty protocol:
	// tmux's modifyOtherKeys implementation mangles shift+enter into ctrl+j
	// (\x1b[27;5;106~) and takes precedence over the Kitty protocol when
	// both are active. With Kitty-only, shift+enter correctly produces \n.
	if !(inTmux && kittyCompat) {
		os.Stdout.WriteString("\x1b[>4;2m")
	}

	// For terminals that implement the Kitty keyboard protocol (Ghostty,
	// WezTerm, Kitty itself) also send the protocol activation escape.
	// Flag 1 = "disambiguate escape codes" — the minimum needed to get
	// unambiguous sequences like \x1b[13;2u for Shift+Enter.
	// This is a no-op on terminals that don't support it.
	if kittyCompat {
		os.Stdout.WriteString("\x1b[>1u")
		SetKittyProtocolActive(true)
	}

	// Set up SIGWINCH handler for resize
	t.sigwinchCh = make(chan os.Signal, 1)
	signal.Notify(t.sigwinchCh, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-t.sigwinchCh:
				t.mu.Lock()
				handler := t.resizeHandler
				t.mu.Unlock()
				if handler != nil {
					handler()
				}
			case <-t.stopCh:
				return
			}
		}
	}()

	// Start reading stdin
	go t.readInput()
}

func (t *ProcessTerminal) readInput() {
	buf := make([]byte, 4096)
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			data := string(buf[:n])
			t.mu.Lock()
			handler := t.inputHandler
			t.mu.Unlock()
			if handler != nil {
				handler(data)
			}
		}
	}
}

// Stop disables raw mode and restores terminal state.
func (t *ProcessTerminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}
	t.stopped = true

	// Disable bracketed paste mode
	os.Stdout.WriteString("\x1b[?2004l")

	// Disable modifyOtherKeys (safe even if we didn't enable it)
	os.Stdout.WriteString("\x1b[>4m")

	// Disable Kitty keyboard protocol if we enabled it
	if IsKittyProtocolActive() {
		os.Stdout.WriteString("\x1b[<u")
		SetKittyProtocolActive(false)
	}

	// Signal goroutines to stop
	close(t.stopCh)

	// Stop SIGWINCH
	if t.sigwinchCh != nil {
		signal.Stop(t.sigwinchCh)
	}

	// Clear handlers
	t.inputHandler = nil
	t.resizeHandler = nil

	// Restore terminal state
	if t.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), t.oldState)
	}
}

// DrainInput drains buffered stdin to prevent leaked escape sequences.
// It suppresses the input handler for idleMs milliseconds, which is enough
// time for any in-flight read to complete. maxMs is kept for interface
// compatibility but is not used (since we cannot observe actual data arrival
// while the handler is nil).
func (t *ProcessTerminal) DrainInput(maxMs, idleMs int) {
	if idleMs <= 0 {
		idleMs = 50
	}

	// Temporarily suppress input handling while draining.
	t.mu.Lock()
	prevHandler := t.inputHandler
	t.inputHandler = nil
	t.mu.Unlock()

	time.Sleep(time.Duration(idleMs) * time.Millisecond)

	// Restore handler.
	t.mu.Lock()
	t.inputHandler = prevHandler
	t.mu.Unlock()
}

// Write writes data to stdout.
func (t *ProcessTerminal) Write(data string) {
	os.Stdout.WriteString(data)
}

// Columns returns the terminal width.
func (t *ProcessTerminal) Columns() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// Rows returns the terminal height.
func (t *ProcessTerminal) Rows() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || h <= 0 {
		return 24
	}
	return h
}

// MoveBy moves cursor up (negative) or down (positive) by N lines.
func (t *ProcessTerminal) MoveBy(lines int) {
	if lines > 0 {
		os.Stdout.WriteString(fmt.Sprintf("\x1b[%dB", lines))
	} else if lines < 0 {
		os.Stdout.WriteString(fmt.Sprintf("\x1b[%dA", -lines))
	}
}

// HideCursor hides the terminal cursor.
func (t *ProcessTerminal) HideCursor() {
	os.Stdout.WriteString("\x1b[?25l")
}

// ShowCursor shows the terminal cursor.
func (t *ProcessTerminal) ShowCursor() {
	os.Stdout.WriteString("\x1b[?25h")
}

// ClearLine clears the current line.
func (t *ProcessTerminal) ClearLine() {
	os.Stdout.WriteString("\x1b[K")
}

// ClearFromCursor clears from cursor to end of screen.
func (t *ProcessTerminal) ClearFromCursor() {
	os.Stdout.WriteString("\x1b[J")
}

// ClearScreen clears the entire screen and moves cursor to (0,0).
func (t *ProcessTerminal) ClearScreen() {
	os.Stdout.WriteString("\x1b[2J\x1b[H")
}

// SetTitle sets the terminal window title.
func (t *ProcessTerminal) SetTitle(title string) {
	os.Stdout.WriteString(fmt.Sprintf("\x1b]0;%s\x07", title))
}

// MockTerminal is a terminal for testing that captures output.
type MockTerminal struct {
	mu           sync.Mutex
	output       []string
	cols         int
	rows         int
	inputHandler func(data string)
	resizeHandler func()
}

// NewMockTerminal creates a MockTerminal with the given dimensions.
func NewMockTerminal(cols, rows int) *MockTerminal {
	return &MockTerminal{cols: cols, rows: rows}
}

func (t *MockTerminal) Start(onInput func(data string), onResize func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputHandler = onInput
	t.resizeHandler = onResize
}

func (t *MockTerminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputHandler = nil
	t.resizeHandler = nil
}

func (t *MockTerminal) DrainInput(maxMs, idleMs int) {}

func (t *MockTerminal) Write(data string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, data)
}

func (t *MockTerminal) Columns() int { return t.cols }
func (t *MockTerminal) Rows() int    { return t.rows }

func (t *MockTerminal) MoveBy(lines int)      {}
func (t *MockTerminal) HideCursor()            {}
func (t *MockTerminal) ShowCursor()            {}
func (t *MockTerminal) ClearLine()             {}
func (t *MockTerminal) ClearFromCursor()       {}
func (t *MockTerminal) ClearScreen()           {}
func (t *MockTerminal) SetTitle(title string)  {}

// SimulateInput sends input data to the terminal's input handler.
func (t *MockTerminal) SimulateInput(data string) {
	t.mu.Lock()
	handler := t.inputHandler
	t.mu.Unlock()
	if handler != nil {
		handler(data)
	}
}

// SimulateResize triggers the resize handler.
func (t *MockTerminal) SimulateResize() {
	t.mu.Lock()
	handler := t.resizeHandler
	t.mu.Unlock()
	if handler != nil {
		handler()
	}
}

// SetSize changes the mock terminal dimensions.
func (t *MockTerminal) SetSize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols = cols
	t.rows = rows
}

// GetOutput returns all written output.
func (t *MockTerminal) GetOutput() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]string, len(t.output))
	copy(result, t.output)
	return result
}

// ClearOutput clears the captured output.
func (t *MockTerminal) ClearOutput() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = nil
}
