package acp

import (
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/session"
)

// countInProgressToolUpdates counts tool_call updates carrying status
// "in_progress" for the given tool call id.
func countInProgressToolUpdates(updates []acpsdk.SessionNotification, toolCallID string) int {
	n := 0
	for _, u := range updates {
		tc := u.Update.ToolCallUpdate
		if tc == nil || string(tc.ToolCallId) != toolCallID {
			continue
		}
		if tc.Status != nil && *tc.Status == acpsdk.ToolCallStatus("in_progress") {
			n++
		}
	}
	return n
}

// A long, silent tool call must keep emitting tool_call updates so a relay's
// wedged-turn watchdog (e.g. poe-acp -idle-write-timeout) sees progress.
func TestToolHeartbeat_EmitsWhileToolRuns(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "20ms")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-1",
			ToolName:   "Bash",
			Args:       map[string]any{"command": "sleep 600"},
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countInProgressToolUpdates(mock.getUpdates(), "tc-1") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := countInProgressToolUpdates(mock.getUpdates(), "tc-1"); got < 2 {
		t.Fatalf("expected >= 2 in_progress heartbeats for a long-running tool, got %d", got)
	}
	if pa.activeToolHeartbeats() != 1 {
		t.Fatalf("expected 1 active heartbeat, got %d", pa.activeToolHeartbeats())
	}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc-1",
			ToolName:   "Bash",
			Result:     agent.AgentToolResult{},
		},
	})

	if pa.activeToolHeartbeats() != 0 {
		t.Fatalf("heartbeat must stop when the tool finishes, still active: %d", pa.activeToolHeartbeats())
	}

	before := countInProgressToolUpdates(mock.getUpdates(), "tc-1")
	time.Sleep(80 * time.Millisecond)
	if after := countInProgressToolUpdates(mock.getUpdates(), "tc-1"); after != before {
		t.Fatalf("heartbeat kept firing after tool end: %d -> %d", before, after)
	}
}

// A zero/negative interval disables the mechanism entirely.
func TestToolHeartbeat_DisabledByEnv(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-2",
			ToolName:   "Bash",
			Args:       map[string]any{"command": "sleep 600"},
		},
	})

	time.Sleep(60 * time.Millisecond)
	if n := pa.activeToolHeartbeats(); n != 0 {
		t.Fatalf("heartbeats must be disabled, got %d active", n)
	}
	if got := countInProgressToolUpdates(mock.getUpdates(), "tc-2"); got != 0 {
		t.Fatalf("expected no heartbeat updates when disabled, got %d", got)
	}
}

// The end of a turn must not leave heartbeat goroutines behind, even if a
// tool's end event never arrives (cancelled turn, transport error).
func TestToolHeartbeat_StoppedOnAgentEnd(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "20ms")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-3",
			ToolName:   "Bash",
			Args:       map[string]any{"command": "sleep 600"},
		},
	})
	if pa.activeToolHeartbeats() != 1 {
		t.Fatalf("expected heartbeat to start, got %d", pa.activeToolHeartbeats())
	}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{Type: agent.EventAgentEnd},
	})

	if n := pa.activeToolHeartbeats(); n != 0 {
		t.Fatalf("agent end must clear heartbeats, got %d", n)
	}
}

// A tool that streams partial output produces an immediate in_progress
// update, so progress is visible without waiting for the next heartbeat tick.
func TestToolExecutionUpdate_EmitsInProgress(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:          agent.EventToolExecutionUpdate,
			ToolCallID:    "tc-4",
			ToolName:      "Bash",
			StatusMessage: "building…",
		},
	})

	if got := countInProgressToolUpdates(mock.getUpdates(), "tc-4"); got != 1 {
		t.Fatalf("expected 1 in_progress update from a tool progress event, got %d", got)
	}
}
