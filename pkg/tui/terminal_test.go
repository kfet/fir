package tui

import (
	"sync"
	"testing"
)

func TestMockTerminal_Dimensions(t *testing.T) {
	m := NewMockTerminal(120, 40)
	if m.Columns() != 120 {
		t.Errorf("expected 120 columns, got %d", m.Columns())
	}
	if m.Rows() != 40 {
		t.Errorf("expected 40 rows, got %d", m.Rows())
	}
}

func TestMockTerminal_SetSize(t *testing.T) {
	m := NewMockTerminal(80, 24)
	m.SetSize(160, 50)
	if m.Columns() != 160 || m.Rows() != 50 {
		t.Errorf("expected 160x50, got %dx%d", m.Columns(), m.Rows())
	}
}

func TestMockTerminal_Write(t *testing.T) {
	m := NewMockTerminal(80, 24)
	m.Write("hello")
	m.Write("world")
	output := m.GetOutput()
	if len(output) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(output))
	}
	if output[0] != "hello" || output[1] != "world" {
		t.Errorf("unexpected output: %v", output)
	}
}

func TestMockTerminal_ClearOutput(t *testing.T) {
	m := NewMockTerminal(80, 24)
	m.Write("test")
	m.ClearOutput()
	if len(m.GetOutput()) != 0 {
		t.Error("expected empty output after clear")
	}
}

func TestMockTerminal_SimulateInput(t *testing.T) {
	m := NewMockTerminal(80, 24)
	var received string
	var mu sync.Mutex

	m.Start(func(data string) {
		mu.Lock()
		received = data
		mu.Unlock()
	}, func() {})

	m.SimulateInput("hello")

	mu.Lock()
	r := received
	mu.Unlock()

	if r != "hello" {
		t.Errorf("expected 'hello', got %q", r)
	}

	m.Stop()
}

func TestMockTerminal_SimulateResize(t *testing.T) {
	m := NewMockTerminal(80, 24)
	resized := false
	var mu sync.Mutex

	m.Start(func(data string) {}, func() {
		mu.Lock()
		resized = true
		mu.Unlock()
	})

	m.SimulateResize()

	mu.Lock()
	r := resized
	mu.Unlock()

	if !r {
		t.Error("expected resize handler to be called")
	}

	m.Stop()
}

func TestMockTerminal_StopClearsHandlers(t *testing.T) {
	m := NewMockTerminal(80, 24)
	called := false

	m.Start(func(data string) {
		called = true
	}, func() {})

	m.Stop()

	// Input after stop should not call handler
	m.SimulateInput("test")
	if called {
		t.Error("handler should not be called after Stop()")
	}
}

func TestMockTerminal_NoOps(t *testing.T) {
	m := NewMockTerminal(80, 24)
	// These should not panic
	m.MoveBy(5)
	m.MoveBy(-3)
	m.HideCursor()
	m.ShowCursor()
	m.ClearLine()
	m.ClearFromCursor()
	m.ClearScreen()
	m.SetTitle("test")
	m.DrainInput(100, 10)
}

func TestProcessTerminal_Implements(t *testing.T) {
	// Verify ProcessTerminal implements Terminal interface
	var _ Terminal = &ProcessTerminal{}
	var _ Terminal = &MockTerminal{}
}

func TestIsKittyCompatibleTerminal_KittyWindowID(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "42")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true when KITTY_WINDOW_ID is set")
	}
}

func TestIsKittyCompatibleTerminal_Ghostty(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "Ghostty")
	t.Setenv("TERM", "")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for TERM_PROGRAM=Ghostty")
	}
}

func TestIsKittyCompatibleTerminal_GhosttyLowercase(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("TERM", "")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for TERM_PROGRAM=ghostty (case-insensitive)")
	}
}

func TestIsKittyCompatibleTerminal_WezTerm(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "WezTerm")
	t.Setenv("TERM", "")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for TERM_PROGRAM=WezTerm")
	}
}

func TestIsKittyCompatibleTerminal_XtermKitty(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "xterm-kitty")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for TERM=xterm-kitty")
	}
}

func TestIsKittyCompatibleTerminal_Unknown(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("GHOSTTY_BIN_DIR", "")
	t.Setenv("WEZTERM_EXECUTABLE", "")
	if isKittyCompatibleTerminal() {
		t.Error("expected false for an ordinary terminal")
	}
}

func TestIsKittyCompatibleTerminal_GhosttyViaTmux(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "tmux")
	t.Setenv("TERM", "screen-256color")
	t.Setenv("GHOSTTY_BIN_DIR", "/Applications/Ghostty.app/Contents/MacOS")
	t.Setenv("WEZTERM_EXECUTABLE", "")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for Ghostty detected via GHOSTTY_BIN_DIR inside tmux")
	}
}

func TestIsKittyCompatibleTerminal_WezTermViaTmux(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM_PROGRAM", "tmux")
	t.Setenv("TERM", "screen-256color")
	t.Setenv("GHOSTTY_BIN_DIR", "")
	t.Setenv("WEZTERM_EXECUTABLE", "/usr/bin/wezterm")
	if !isKittyCompatibleTerminal() {
		t.Error("expected true for WezTerm detected via WEZTERM_EXECUTABLE inside tmux")
	}
}
