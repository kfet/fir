package components

import (
	"strings"
	"sync"
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
	frames := []string{"|", "/", "-", "\\"}
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

func TestLoader_AppendsElapsedAfterThreshold(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "Inferring...")
	defer l.Stop()

	// Advance the virtual clock past the threshold.
	base := l.StartedAt()
	l.SetClock(func() time.Time { return base.Add(45 * time.Second) })

	l.mu.Lock()
	l.updateDisplay()
	l.mu.Unlock()

	lines := l.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Inferring... 45s") {
		t.Errorf("expected elapsed counter '45s' appended, got %q", joined)
	}
}

func TestLoader_NoElapsedBeforeThreshold(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "Inferring...")
	defer l.Stop()

	base := l.StartedAt()
	l.SetClock(func() time.Time { return base.Add(10 * time.Second) })
	l.mu.Lock()
	l.updateDisplay()
	l.mu.Unlock()

	lines := l.Render(80)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "10s") {
		t.Errorf("expected no elapsed counter before threshold, got %q", joined)
	}
}

func TestLoader_SetMessageResetsElapsed(t *testing.T) {
	ui := &mockUI{}
	l := NewLoader(ui, identity, identity, "phase1")
	defer l.Stop()

	base := l.StartedAt()
	// Simulate 50s elapsed, then SetMessage — clock advances by another 5s.
	var nowMu sync.Mutex
	now := base.Add(50 * time.Second)
	l.SetClock(func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	})
	l.SetMessage("phase2")
	// startedAt is now reset to 'now'. Advance only 5s more — below threshold.
	nowMu.Lock()
	now = now.Add(5 * time.Second)
	nowMu.Unlock()
	l.mu.Lock()
	l.updateDisplay()
	l.mu.Unlock()

	lines := l.Render(80)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "5s") || strings.Contains(joined, "55s") {
		t.Errorf("SetMessage should reset elapsed; got %q", joined)
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m00s"},
		{125 * time.Second, "2m05s"},
		{3600 * time.Second, "1h00m"},
		{3660 * time.Second, "1h01m"},
	}
	for _, c := range cases {
		got := formatElapsed(c.in)
		if got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
