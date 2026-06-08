package acp

import (
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/mcp"
)

// newReleasableSession builds a firSession wired with observable teardown
// side effects: an unsubscribe flag, a real (closeable) MCP manager, and a
// closed extReady channel.
func newReleasableSession(t *testing.T) (*firSession, *bool, *mcp.Manager) {
	t.Helper()
	unsub := false
	mgr := mcp.NewManager(nil, false)
	extReady := make(chan struct{})
	close(extReady)
	entry := &firSession{
		session:     newMinimalSession(t),
		termState:   newTerminalState(),
		mcpManager:  mgr,
		extReady:    extReady,
		unsubscribe: func() { unsub = true },
	}
	return entry, &unsub, mgr
}

func TestReleaseSession_TearsDownAndRemoves(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry, unsub, mgr := newReleasableSession(t)
	pa.sessions["s1"] = entry

	resp, err := pa.ReleaseSession(context.Background(), ReleaseSessionRequest{SessionId: "s1"})
	if err != nil {
		t.Fatalf("ReleaseSession error: %v", err)
	}
	_ = resp

	// Removed from the map.
	pa.mu.Lock()
	_, present := pa.sessions["s1"]
	pa.mu.Unlock()
	if present {
		t.Error("session still present in map after release")
	}
	// unsubscribe called.
	if !*unsub {
		t.Error("unsubscribe was not called during teardown")
	}
	// MCP manager closed (Done channel closed).
	select {
	case <-mgr.Done():
	case <-time.After(2 * time.Second):
		t.Error("mcpManager.Close was not called during teardown")
	}
}

func TestReleaseSession_UnknownID_TypedError(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	_, err := pa.ReleaseSession(context.Background(), ReleaseSessionRequest{SessionId: "ghost"})
	re, ok := err.(*acpsdk.RequestError)
	if !ok {
		t.Fatalf("expected *acpsdk.RequestError, got %T (%v)", err, err)
	}
	if re.Code != SessionNotFoundError {
		t.Errorf("code = %d, want %d", re.Code, SessionNotFoundError)
	}
}

func TestPrompt_ReleasedID_TypedError(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry, _, _ := newReleasableSession(t)
	pa.sessions["s1"] = entry

	if _, err := pa.ReleaseSession(context.Background(), ReleaseSessionRequest{SessionId: "s1"}); err != nil {
		t.Fatalf("release: %v", err)
	}

	_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: "s1",
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	re, ok := err.(*acpsdk.RequestError)
	if !ok {
		t.Fatalf("expected *acpsdk.RequestError, got %T (%v)", err, err)
	}
	if re.Code != SessionNotFoundError {
		t.Errorf("code = %d, want %d", re.Code, SessionNotFoundError)
	}
}

func TestReapIdle_ReapsIdleSparesActive(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	mc := newMockConn()
	pa := &firAgent{
		conn:     mc,
		sessions: make(map[string]*firSession),
		idleTTL:  30 * time.Minute,
		nowFn:    func() time.Time { return base },
	}

	idle, _, idleMgr := newReleasableSession(t)
	idle.touch(base.Add(-time.Hour)) // older than cutoff → reaped
	pa.sessions["idle"] = idle

	active, _, _ := newReleasableSession(t)
	active.touch(base.Add(-time.Minute)) // within TTL → spared
	pa.sessions["active"] = active

	reaped := pa.reapIdle(pa.now())
	if len(reaped) != 1 || reaped[0] != "idle" {
		t.Fatalf("reaped = %v, want [idle]", reaped)
	}

	pa.mu.Lock()
	_, idlePresent := pa.sessions["idle"]
	_, activePresent := pa.sessions["active"]
	pa.mu.Unlock()
	if idlePresent {
		t.Error("idle session was not removed")
	}
	if !activePresent {
		t.Error("active session was incorrectly reaped")
	}
	// Idle session's MCP manager was closed.
	select {
	case <-idleMgr.Done():
	case <-time.After(2 * time.Second):
		t.Error("idle session MCP manager was not closed")
	}
}

func TestReapIdle_Disabled(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession), idleTTL: 0}
	entry, _, _ := newReleasableSession(t)
	entry.touch(time.Unix(0, 0)) // ancient
	pa.sessions["s1"] = entry
	if got := pa.reapIdle(time.Now()); got != nil {
		t.Errorf("reapIdle with TTL=0 returned %v, want nil", got)
	}
	pa.mu.Lock()
	_, present := pa.sessions["s1"]
	pa.mu.Unlock()
	if !present {
		t.Error("session reaped despite disabled reaper")
	}
}

func TestTeardownSession_EmitsExtShutdown(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeCommandExtScript(t, dir)
	mgr, _ := startExtManager(t, dir, scriptPath)

	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	extReady := make(chan struct{})
	close(extReady)
	entry := &firSession{
		session:   newMinimalSession(t),
		termState: newTerminalState(),
		extSetup:  &extension.SetupResult{Manager: mgr},
		extReady:  extReady,
	}

	pa.teardownSession(context.Background(), "s1", entry)

	// After EmitSessionShutdown → Manager.Stop, all bridges are gone so no
	// commands remain.
	if cmds := mgr.GetCommands(); len(cmds) != 0 {
		t.Errorf("expected 0 commands after teardown, got %d", len(cmds))
	}
}

func TestTeardownSession_NilEntry(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	// Must not panic.
	pa.teardownSession(context.Background(), "s1", nil)
}

func TestIdleReaper_GoroutineReaps(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	tickCh := make(chan time.Time, 1)
	mc := newMockConn()
	pa := &firAgent{
		conn:         mc,
		sessions:     make(map[string]*firSession),
		idleTTL:      30 * time.Minute,
		nowFn:        func() time.Time { return base },
		reaperTick:   tickCh,
		reaperNotify: make(chan struct{}, 1),
	}

	idle, _, _ := newReleasableSession(t)
	idle.touch(base.Add(-time.Hour))
	pa.sessions["idle"] = idle

	pa.startIdleReaper(time.Hour) // interval ignored: reaperTick is set
	defer pa.stopIdleReaper()

	tickCh <- base
	select {
	case <-pa.reaperNotify:
	case <-time.After(3 * time.Second):
		t.Fatal("reaper did not run within 3s")
	}

	pa.mu.Lock()
	_, present := pa.sessions["idle"]
	pa.mu.Unlock()
	if present {
		t.Error("idle session not reaped by goroutine")
	}
}

func TestStartIdleReaper_DisabledNoop(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession), idleTTL: 0}
	pa.startIdleReaper(time.Hour)
	if pa.stopReaper != nil {
		t.Error("reaper started despite TTL=0")
	}
	// stopIdleReaper must be a no-op (no panic, no block).
	pa.stopIdleReaper()
}

func TestReaperIntervalFor(t *testing.T) {
	cases := []struct {
		ttl  time.Duration
		want time.Duration
	}{
		{time.Hour, time.Minute},              // capped at reaperInterval
		{10 * time.Minute, time.Minute},       // half=5m, capped at 1m
		{90 * time.Second, 45 * time.Second},  // half=45s < 1m
		{2 * time.Second, time.Second},        // half=1s, floor at 1s
		{500 * time.Millisecond, time.Second}, // half<1s, floored to 1s
	}
	for _, c := range cases {
		if got := reaperIntervalFor(c.ttl); got != c.want {
			t.Errorf("reaperIntervalFor(%v) = %v, want %v", c.ttl, got, c.want)
		}
	}
}

func TestStartIdleReaper_RealTicker(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{
		conn:         mc,
		sessions:     make(map[string]*firSession),
		idleTTL:      time.Hour,
		reaperNotify: make(chan struct{}, 1),
	}
	// Real internal ticker (reaperTick nil) with a tiny interval so the tick
	// branch fires quickly. No sessions, so each pass is a no-op reap.
	pa.startIdleReaper(10 * time.Millisecond)
	select {
	case <-pa.reaperNotify:
	case <-time.After(3 * time.Second):
		t.Fatal("real-ticker reaper did not run within 3s")
	}
	pa.stopIdleReaper()
}
