package tmuxspinner

import (
	"sync"
	"testing"
	"time"

	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/extension"
)

// recorder collects rename calls thread-safely.
type recorder struct {
	mu    sync.Mutex
	names []string
}

func (r *recorder) record(name string) {
	r.mu.Lock()
	r.names = append(r.names, name)
	r.mu.Unlock()
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.names))
	copy(out, r.names)
	return out
}

func (r *recorder) clear() {
	r.mu.Lock()
	r.names = nil
	r.mu.Unlock()
}

// testSetup overrides package-level functions for testing and returns
// a recorder and cleanup function.
func testSetup(tmux bool, paneID, windowName string) (*recorder, func()) {
	origIsTmux := isTmux
	origReadPaneID := readPaneID
	origReadWindowName := readWindowName
	origRenameWindow := renameWindow
	origDisableAutoRename := disableAutoRename

	rec := &recorder{}

	isTmux = func() bool { return tmux }
	readPaneID = func() string { return paneID }
	readWindowName = func(_ string) string { return windowName }
	renameWindow = func(_ string, name string) { rec.record(name) }
	disableAutoRename = func(_ string) {}

	return rec, func() {
		isTmux = origIsTmux
		readPaneID = origReadPaneID
		readWindowName = origReadWindowName
		renameWindow = origRenameWindow
		disableAutoRename = origDisableAutoRename
	}
}

func TestExtensionRegisters(t *testing.T) {
	factories := extension.RegisteredFactories()
	found := false
	for _, f := range factories {
		if f.Name == "tmuxspinner" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("tmuxspinner extension not found in registry")
	}
}

func TestExtensionLoads(t *testing.T) {
	_, cleanup := testSetup(true, "%0", "mywin")
	defer cleanup()

	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	if !runner.HasHandlers("agent_start") {
		t.Error("expected agent_start handler")
	}
	if !runner.HasHandlers("agent_end") {
		t.Error("expected agent_end handler")
	}
	if !runner.HasHandlers("session_shutdown") {
		t.Error("expected session_shutdown handler")
	}
}

func TestNoopWhenNotInTmux(t *testing.T) {
	_, cleanup := testSetup(false, "", "")
	defer cleanup()

	// Re-register to pick up isTmux=false
	extension.ClearRegistry()
	extension.Register("tmuxspinner", factory)

	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// No handlers should be registered when not in tmux
	if runner.HasHandlers("agent_start") {
		t.Error("should not register handlers when not in tmux")
	}
}

func TestSpinnerStartStop(t *testing.T) {
	rec, cleanup := testSetup(true, "%0", "mywin")
	defer cleanup()

	s := &spinner{}

	s.Start()
	// Let it run a few frames
	time.Sleep(500 * time.Millisecond)
	s.Stop()

	names := rec.get()

	if len(names) == 0 {
		t.Fatal("expected rename calls, got none")
	}

	// Should contain spinner frames with base name
	found := false
	for _, n := range names {
		if len(n) > 5 && n[:5] == "mywin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected renames to contain base name 'mywin', got %v", names)
	}

	// After stop, last rename should restore the base name
	last := names[len(names)-1]
	if last != "mywin" {
		t.Errorf("expected last rename to restore 'mywin', got %q", last)
	}
}

func TestSpinnerStartIdempotent(t *testing.T) {
	rec, cleanup := testSetup(true, "%0", "win")
	defer cleanup()

	s := &spinner{}
	s.Start()
	s.Start() // should be no-op
	s.Start() // should be no-op
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	// Should not panic, and renames should be sane
	names := rec.get()
	if len(names) == 0 {
		t.Error("expected some renames")
	}
}

func TestSpinnerStopIdempotent(t *testing.T) {
	_, cleanup := testSetup(true, "%0", "win")
	defer cleanup()

	s := &spinner{}
	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()
	s.Stop() // should be no-op, no panic
}

func TestSpinnerRestartable(t *testing.T) {
	rec, cleanup := testSetup(true, "%0", "win")
	defer cleanup()

	s := &spinner{}

	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	rec.clear()

	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	names := rec.get()
	if len(names) == 0 {
		t.Error("expected renames after restart")
	}
	last := names[len(names)-1]
	if last != "win" {
		t.Errorf("expected restore after second stop, got %q", last)
	}
}

func TestSpinnerConcurrentStartStop(t *testing.T) {
	_, cleanup := testSetup(true, "%0", "race")
	defer cleanup()

	s := &spinner{}
	var wg sync.WaitGroup

	// Hammer start/stop concurrently to check for races
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Start()
		}()
		go func() {
			defer wg.Done()
			s.Stop()
		}()
	}
	wg.Wait()
	s.Stop() // ensure stopped
}

func TestDefaultBaseName(t *testing.T) {
	rec, cleanup := testSetup(true, "%0", "") // empty window name
	defer cleanup()

	s := &spinner{}
	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	names := rec.get()
	// Should fall back to "tau" as base name
	found := false
	for _, n := range names {
		if len(n) >= 3 && n[:3] == "tau" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected fallback base name 'tau' in renames, got: %v", names)
	}
}

func TestNoopWhenPaneIDEmpty(t *testing.T) {
	rec, cleanup := testSetup(true, "", "mywin") // empty pane ID
	defer cleanup()

	s := &spinner{}
	s.Start()
	time.Sleep(200 * time.Millisecond)
	// Start should have bailed out; spinner should not be running
	if s.running {
		s.Stop()
		t.Error("spinner should not start when pane ID is empty")
	}

	names := rec.get()
	if len(names) != 0 {
		t.Errorf("expected no rename calls when pane ID is empty, got %v", names)
	}
}

func TestRenameTargetsCorrectPane(t *testing.T) {
	// Verify that all tmux calls target the captured pane ID
	origIsTmux := isTmux
	origReadPaneID := readPaneID
	origReadWindowName := readWindowName
	origRenameWindow := renameWindow
	origDisableAutoRename := disableAutoRename
	defer func() {
		isTmux = origIsTmux
		readPaneID = origReadPaneID
		readWindowName = origReadWindowName
		renameWindow = origRenameWindow
		disableAutoRename = origDisableAutoRename
	}()

	var mu sync.Mutex
	var targets []string

	isTmux = func() bool { return true }
	readPaneID = func() string { return "%42" }
	readWindowName = func(target string) string {
		mu.Lock()
		targets = append(targets, target)
		mu.Unlock()
		return "mywin"
	}
	renameWindow = func(target, _ string) {
		mu.Lock()
		targets = append(targets, target)
		mu.Unlock()
	}
	disableAutoRename = func(target string) {
		mu.Lock()
		targets = append(targets, target)
		mu.Unlock()
	}

	s := &spinner{}
	s.Start()
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	mu.Lock()
	defer mu.Unlock()

	for i, tgt := range targets {
		if tgt != "%42" {
			t.Errorf("targets[%d] = %q, want %q", i, tgt, "%42")
		}
	}
	if len(targets) == 0 {
		t.Error("expected some tmux calls")
	}
}
