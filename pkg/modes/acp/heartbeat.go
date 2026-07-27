package acp

import (
	"context"
	"os"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// defaultToolHeartbeatInterval is how often an in-flight tool call emits a
// keepalive `tool_call` update while it is otherwise silent.
//
// Relays put a wedged-turn watchdog in front of the agent. poe-acp's
// -idle-write-timeout (default 2m) cancels a turn that produces no agent
// output within the window, and — per its own documentation — plain SSE
// heartbeat keepalives do NOT reset it; only a real tool_call update does.
// A single long, quiet tool call (a slow ssh, a build, a large grep on a
// remote host) was therefore indistinguishable from a hung agent: the relay
// cancelled the turn mid-work and fir recorded an assistant message with
// StopReason=error / "http request: ... context canceled". 30s sits an order
// of magnitude under the tightest watchdog window we know of.
const defaultToolHeartbeatInterval = 30 * time.Second

// toolHeartbeatInterval resolves the heartbeat period. FIR_ACP_TOOL_HEARTBEAT
// accepts any time.ParseDuration value; a value <= 0 disables heartbeats.
func toolHeartbeatInterval() time.Duration {
	if v := os.Getenv("FIR_ACP_TOOL_HEARTBEAT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultToolHeartbeatInterval
}

// heartbeatKey namespaces an in-flight tool call by session. The NUL byte
// cannot appear in either id, so the key is unambiguous.
func heartbeatKey(sessionID, toolCallID string) string {
	return sessionID + "\x00" + toolCallID
}

// heartbeats guards lazy initialisation of the in-flight heartbeat registry.
// Callers must hold pa.mu.
func (pa *firAgent) heartbeatsLocked() map[string]chan struct{} {
	if pa.toolHeartbeats == nil {
		pa.toolHeartbeats = make(map[string]chan struct{})
	}
	return pa.toolHeartbeats
}

// startToolHeartbeat begins emitting periodic in_progress tool_call updates
// for toolCallID until stopToolHeartbeat (or stopSessionHeartbeats) is called.
// Starting a heartbeat that is already running is a no-op.
func (pa *firAgent) startToolHeartbeat(sessionID string, entry *firSession, toolCallID string) {
	interval := toolHeartbeatInterval()
	if interval <= 0 || pa.conn == nil || entry == nil || toolCallID == "" {
		return
	}

	key := heartbeatKey(sessionID, toolCallID)
	stop := make(chan struct{})

	pa.mu.Lock()
	hb := pa.heartbeatsLocked()
	if _, running := hb[key]; running {
		pa.mu.Unlock()
		return
	}
	hb[key] = stop
	pa.mu.Unlock()

	go pa.runToolHeartbeat(sessionID, entry, toolCallID, interval, stop)
}

func (pa *firAgent) runToolHeartbeat(sessionID string, entry *firSession, toolCallID string, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Keep the session out of the idle reaper's sights too: a long
			// tool call is activity, not idleness.
			entry.touch(pa.now())
			_ = pa.conn.SessionUpdate(context.Background(), entry.notification(
				acpsdk.SessionId(sessionID),
				acpsdk.UpdateToolCall(
					acpsdk.ToolCallId(toolCallID),
					acpsdk.WithUpdateStatus("in_progress"),
				),
			))
		}
	}
}

// stopToolHeartbeat halts the heartbeat for a single tool call. Safe to call
// for a tool call that never started one.
func (pa *firAgent) stopToolHeartbeat(sessionID, toolCallID string) {
	key := heartbeatKey(sessionID, toolCallID)
	pa.mu.Lock()
	stop, ok := pa.toolHeartbeats[key]
	if ok {
		delete(pa.toolHeartbeats, key)
	}
	pa.mu.Unlock()
	if ok {
		close(stop)
	}
}

// stopSessionHeartbeats halts every heartbeat belonging to a session. Called
// when a turn ends (including on cancel/error) and when a session is released
// or reaped, so no goroutine outlives the work it was reporting on.
func (pa *firAgent) stopSessionHeartbeats(sessionID string) {
	prefix := sessionID + "\x00"
	var stops []chan struct{}
	pa.mu.Lock()
	for key, stop := range pa.toolHeartbeats {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			stops = append(stops, stop)
			delete(pa.toolHeartbeats, key)
		}
	}
	pa.mu.Unlock()
	for _, stop := range stops {
		close(stop)
	}
}

// activeToolHeartbeats reports how many heartbeats are currently running.
// Used by tests.
func (pa *firAgent) activeToolHeartbeats() int {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return len(pa.toolHeartbeats)
}
