package session

import "testing"

// TestAwaitTurnReady_RunsBeforeTurnAfterExtReady: the BeforeTurn hook (which
// resumes idle-stopped extensions) runs on every turn, only once extensions
// finished loading.
func TestAwaitTurnReady_RunsBeforeTurnAfterExtReady(t *testing.T) {
	ready := make(chan struct{})
	s := &AgentSession{extReady: ready}
	calls := 0
	s.hooks.Store(&AgentSessionHooks{BeforeTurn: func() {
		select {
		case <-ready:
		default:
			t.Error("BeforeTurn ran before extensions were ready")
		}
		calls++
	}})
	done := make(chan struct{})
	go func() { s.awaitTurnReady(); close(done) }()
	close(ready)
	<-done
	s.awaitTurnReady()
	if calls != 2 {
		t.Fatalf("BeforeTurn calls = %d, want 2", calls)
	}
	(&AgentSession{}).awaitTurnReady() // no hooks: no panic
}
