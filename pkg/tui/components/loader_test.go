package components

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type mockUI struct {
	renderCount atomic.Int64
}

func (m *mockUI) RequestRender() {
	m.renderCount.Add(1)
}

func identity(s string) string { return s }

func TestLoader_Render(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "Loading...")
	defer l.Stop()

	lines := l.Render(80)
	// First line is empty, then text lines
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d", len(lines))
	}
	if lines[0] != "" {
		t.Errorf("first line should be empty, got %q", lines[0])
	}
	found := false
	for _, line := range lines[1:] {
		if strings.Contains(line, "Loading...") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Loading...' in output, got %v", lines)
	}
}

func TestLoader_SetMessage(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "initial")
	defer l.Stop()

	l.SetMessage("updated")
	lines := l.Render(80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "updated") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'updated' in output, got %v", lines)
	}
}

func TestLoader_Animates(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "test")
	defer l.Stop()

	// Wait for some frames
	time.Sleep(200 * time.Millisecond)
	if ui.renderCount.Load() == 0 {
		t.Error("expected render requests from animation")
	}
}

func TestLoader_Stop(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "test")
	l.Stop()

	countBefore := ui.renderCount.Load()
	time.Sleep(200 * time.Millisecond)
	if ui.renderCount.Load() != countBefore {
		t.Error("should not render after stop")
	}
}

func TestLoader_SpinnerFrames(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "msg")
	defer l.Stop()

	lines1 := l.Render(80)
	// Check spinner char is one of the frames
	spinner := lines1[1]
	hasFrame := false
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	for _, f := range frames {
		if strings.Contains(spinner, f) {
			hasFrame = true
			break
		}
	}
	if !hasFrame {
		t.Errorf("expected spinner frame in output, got %q", spinner)
	}
}
