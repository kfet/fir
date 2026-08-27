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

// Clients render the live spinner label from the update's Title, so a tool's
// status message must go out as the Title (as well as the content).
func TestToolExecutionUpdate_StatusMessageBecomesTitle(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:          agent.EventToolExecutionUpdate,
			ToolCallID:    "tc-5",
			ToolName:      "pipe",
			StatusMessage: "rl-reset 7/60",
		},
	})

	var found bool
	for _, u := range mock.getUpdates() {
		tc := u.Update.ToolCallUpdate
		if tc == nil || string(tc.ToolCallId) != "tc-5" {
			continue
		}
		if tc.Title == nil {
			t.Fatalf("tool_call_update for tc-5 has nil Title")
		}
		if *tc.Title != "rl-reset 7/60" {
			t.Fatalf("Title = %q, want %q", *tc.Title, "rl-reset 7/60")
		}
		if len(tc.Content) != 1 {
			t.Fatalf("expected status message content to be kept, got %d items", len(tc.Content))
		}
		found = true
	}
	if !found {
		t.Fatalf("no tool_call_update emitted for tc-5")
	}
}

// A cancelled turn never delivers tool end events; the per-call state kept for
// those tool calls must not outlive the turn.
func TestAgentEnd_ClearsPendingToolState(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-8",
			ToolName:   "Bash",
			Args:       map[string]any{"command": "sleep 600"},
		},
	})
	if _, ok := entry.pendingTitles.Load("tc-8"); !ok {
		t.Fatalf("expected pendingTitles to hold tc-8 while it runs")
	}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{Type: agent.EventAgentEnd},
	})

	if _, ok := entry.pendingTitles.Load("tc-8"); ok {
		t.Fatalf("pendingTitles must be cleared when the turn ends")
	}
	if _, ok := entry.pendingArgs.Load("tc-8"); ok {
		t.Fatalf("pendingArgs must be cleared when the turn ends")
	}
}

// Progress updates overwrite the title, so the completion update must put the
// original title back — a finished call must not read "rl-reset 7/60".
func TestToolExecutionEnd_RestoresTitle(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	start := session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-7",
			ToolName:   "Bash",
			Args:       map[string]any{"command": "make all"},
		},
	}
	pa.handleEvent("s1", entry, start)

	var startTitle string
	for _, u := range mock.getUpdates() {
		if tc := u.Update.ToolCall; tc != nil && string(tc.ToolCallId) == "tc-7" {
			startTitle = tc.Title
		}
	}
	if startTitle == "" {
		t.Fatalf("no tool_call start with a title emitted")
	}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:          agent.EventToolExecutionUpdate,
			ToolCallID:    "tc-7",
			ToolName:      "Bash",
			StatusMessage: "rl-reset 7/60",
		},
	})
	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc-7",
			ToolName:   "Bash",
			Result:     "done",
		},
	})

	updates := mock.getUpdates()
	last := updates[len(updates)-1].Update.ToolCallUpdate
	if last == nil || string(last.ToolCallId) != "tc-7" {
		t.Fatalf("expected final update for tc-7, got %+v", updates[len(updates)-1].Update)
	}
	if last.Title == nil || *last.Title != startTitle {
		t.Fatalf("final Title = %v, want %q", last.Title, startTitle)
	}
	if _, ok := entry.pendingTitles.Load("tc-7"); ok {
		t.Fatalf("pendingTitles must be cleaned up after the call ends")
	}
}

// An update with no status message must not clobber the tool call's title.
func TestToolExecutionUpdate_NoStatusMessageLeavesTitleNil(t *testing.T) {
	t.Setenv("FIR_ACP_TOOL_HEARTBEAT", "0s")

	mock := newMockConn()
	pa := &firAgent{conn: mock, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionUpdate,
			ToolCallID: "tc-6",
			ToolName:   "pipe",
		},
	})

	for _, u := range mock.getUpdates() {
		tc := u.Update.ToolCallUpdate
		if tc == nil || string(tc.ToolCallId) != "tc-6" {
			continue
		}
		if tc.Title != nil {
			t.Fatalf("Title = %q, want nil for an update with no status message", *tc.Title)
		}
	}
}
