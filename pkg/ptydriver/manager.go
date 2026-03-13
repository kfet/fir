package ptydriver

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Session represents a single PTY-backed process.
type Session struct {
	Name   string
	cmd    *exec.Cmd
	ptmx   *os.File
	screen *Screen
	mu     sync.Mutex
	done   chan struct{}
}

// Manager manages a collection of named sessions, analogous to a tmux server.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session // "session:window" -> Session
	groups   map[string][]string // session name -> list of window names
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		groups:   make(map[string][]string),
	}
}

// key returns the internal key for a session:window pair.
func key(session, window string) string {
	return session + ":" + window
}

// New creates a new session with an initial window running the user's shell.
func (m *Manager) New(session, window string) (*Session, error) {
	if window == "" {
		window = "shell"
	}
	return m.newWindow(session, window, "")
}

// NewWindow creates a new window in an existing session.
func (m *Manager) NewWindow(session, window, command string) (*Session, error) {
	return m.newWindow(session, window, command)
}

func (m *Manager) newWindow(session, window, command string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := key(session, window)
	if _, exists := m.sessions[k]; exists {
		return nil, fmt.Errorf("session %q window %q already exists", session, window)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	var cmd *exec.Cmd
	if command != "" {
		cmd = exec.Command(shell, "-c", command)
	} else {
		cmd = exec.Command(shell)
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	screen := NewScreen(50, 200) // generous default

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	s := &Session{
		Name:   k,
		cmd:    cmd,
		ptmx:   ptmx,
		screen: screen,
		done:   make(chan struct{}),
	}

	// Read PTY output into screen buffer.
	go func() {
		defer close(s.done)
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				screen.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	m.sessions[k] = s
	m.groups[session] = append(m.groups[session], window)

	return s, nil
}

// Send sends text followed by Enter to a session window.
func (m *Manager) Send(target, text string) error {
	s, err := m.get(target)
	if err != nil {
		return err
	}
	_, err = io.WriteString(s.ptmx, text+"\n")
	return err
}

// SendRaw sends raw bytes to a session window (for control characters, etc).
func (m *Manager) SendRaw(target string, data []byte) error {
	s, err := m.get(target)
	if err != nil {
		return err
	}
	_, err = s.ptmx.Write(data)
	return err
}

// Capture returns the last n lines of output from a session window.
func (m *Manager) Capture(target string, lines int) (string, error) {
	s, err := m.get(target)
	if err != nil {
		return "", err
	}
	return s.screen.Capture(lines), nil
}

// Wait polls the session output for a regex pattern, returning nil on match
// or an error on timeout.
func (m *Manager) Wait(target, pattern string, timeout time.Duration) error {
	s, err := m.get(target)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}
	deadline := time.After(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			text := s.screen.Capture(20)
			return fmt.Errorf("timeout after %s waiting for %q; last output:\n%s", timeout, pattern, text)
		case <-tick.C:
			text := s.screen.Capture(1000)
			if re.MatchString(text) {
				return nil
			}
		case <-s.done:
			// Process exited — check one more time.
			text := s.screen.Capture(1000)
			if re.MatchString(text) {
				return nil
			}
			return fmt.Errorf("process exited before pattern %q matched", pattern)
		}
	}
}

// List returns session names, or window names for a given session.
func (m *Manager) List(session string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session == "" {
		names := make([]string, 0, len(m.groups))
		for name := range m.groups {
			names = append(names, name)
		}
		return names
	}
	return append([]string(nil), m.groups[session]...)
}

// Kill destroys a session and all its windows.
func (m *Manager) Kill(session string) error {
	m.mu.Lock()
	windows := m.groups[session]
	m.mu.Unlock()

	var errs []string
	for _, w := range windows {
		if err := m.KillWindow(session, w); err != nil {
			errs = append(errs, err.Error())
		}
	}
	m.mu.Lock()
	delete(m.groups, session)
	m.mu.Unlock()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// KillWindow destroys a single window.
func (m *Manager) KillWindow(session, window string) error {
	k := key(session, window)
	m.mu.Lock()
	s, ok := m.sessions[k]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no such window: %s", k)
	}
	delete(m.sessions, k)
	// Remove from groups.
	windows := m.groups[session]
	for i, w := range windows {
		if w == window {
			m.groups[session] = append(windows[:i], windows[i+1:]...)
			break
		}
	}
	if len(m.groups[session]) == 0 {
		delete(m.groups, session)
	}
	m.mu.Unlock()

	s.ptmx.Close()
	s.cmd.Process.Kill()
	s.cmd.Wait()
	return nil
}

// Alive reports whether the process in the target window is still running.
func (m *Manager) Alive(target string) bool {
	s, err := m.get(target)
	if err != nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (m *Manager) get(target string) (*Session, error) {
	// Accept "session:window" or just "session" (uses first window).
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[target]; ok {
		return s, nil
	}
	// Try as session name, use first window.
	if windows, ok := m.groups[target]; ok && len(windows) > 0 {
		k := key(target, windows[0])
		if s, ok := m.sessions[k]; ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no such session/window: %s", target)
}
