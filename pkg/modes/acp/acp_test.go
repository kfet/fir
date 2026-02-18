package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
)

func TestPiAgent_Initialize(t *testing.T) {
	pa := &piAgent{
		sessions: make(map[string]*piSession),
	}

	resp, err := pa.Initialize(context.Background(), acpsdk.InitializeRequest{
		ClientCapabilities: acpsdk.ClientCapabilities{
			Terminal: true,
		},
	})
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "tau" {
		t.Errorf("agent name = %v, want tau", resp.AgentInfo)
	}
	if resp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		t.Errorf("protocol version = %d, want %d", resp.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}

	// Verify capabilities were stored
	if !pa.clientCaps.Terminal {
		t.Error("clientCapabilities.Terminal should be true")
	}
}

func TestPiAgent_SetSessionModel_NotFound(t *testing.T) {
	pa := &piAgent{
		sessions: make(map[string]*piSession),
	}

	_, err := pa.SetSessionModel(context.Background(), acpsdk.SetSessionModelRequest{
		SessionId: "nonexistent",
		ModelId:   "openai/gpt-4o",
	})
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestPiAgent_Cancel_NonexistentSession(t *testing.T) {
	pa := &piAgent{
		sessions: make(map[string]*piSession),
	}
	// Should not panic
	err := pa.Cancel(context.Background(), acpsdk.CancelNotification{
		SessionId: "nonexistent",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuiltInCommands(t *testing.T) {
	cmds := builtInCommands()
	if len(cmds) == 0 {
		t.Fatal("builtInCommands returned empty slice")
	}

	names := make(map[string]bool)
	for _, cmd := range cmds {
		if cmd.Name == "" {
			t.Error("command has empty name")
		}
		if cmd.Description == "" {
			t.Errorf("command %q has empty description", cmd.Name)
		}
		if names[cmd.Name] {
			t.Errorf("duplicate command name: %q", cmd.Name)
		}
		names[cmd.Name] = true
	}

	// Verify expected commands exist
	for _, expected := range []string{"compact", "resume", "continue", "logout", "reload"} {
		if !names[expected] {
			t.Errorf("missing expected command: %q", expected)
		}
	}
}

func TestCreateAcpTools(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	// useClientTerminal=true, useClientFs=false → 9 tools (with bash_output + bash_kill)
	toolList := pa.createAcpTools("/tmp/test", "session-1", true, false, "")

	if len(toolList) != 9 {
		t.Fatalf("expected 9 ACP tools, got %d", len(toolList))
	}

	names := make(map[string]bool)
	for _, tool := range toolList {
		names[tool.Tool.Name] = true
	}

	expected := []string{"read", "bash", "edit", "write", "grep", "find", "ls", "bash_output", "bash_kill"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %q", name)
		}
	}

	// useClientTerminal=false, useClientFs=false → 7 tools (no bash_output/bash_kill, default bash)
	toolList2 := pa.createAcpTools("/tmp/test", "session-1", false, false, "")
	if len(toolList2) != 7 {
		t.Fatalf("expected 7 ACP tools (no terminal), got %d", len(toolList2))
	}

	// useClientTerminal=false, useClientFs=true → 7 tools with ACP read/write/edit
	toolList3 := pa.createAcpTools("/tmp/test", "session-1", false, true, "")
	if len(toolList3) != 7 {
		t.Fatalf("expected 7 ACP tools (fs only), got %d", len(toolList3))
	}
}

func TestHandleEvent_TextDelta(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type: agent.EventMessageUpdate,
			AssistantMessageEvent: &ai.AssistantMessageEvent{
				Type:  "text_delta",
				Delta: "hello world",
			},
		},
	})

	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if string(updates[0].SessionId) != "s1" {
		t.Errorf("session id = %q, want s1", updates[0].SessionId)
	}
}

func TestHandleEvent_ThinkingDelta(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type: agent.EventMessageUpdate,
			AssistantMessageEvent: &ai.AssistantMessageEvent{
				Type:  "thinking_delta",
				Delta: "hmm",
			},
		},
	})

	if len(mc.getUpdates()) != 1 {
		t.Fatal("expected 1 update for thinking_delta")
	}
}

func TestHandleEvent_NilAgentEvent(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	// Should not panic
	pa.handleEvent("s1", entry, core.AgentSessionEvent{AgentEvent: nil})
	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for nil AgentEvent")
	}
}

func TestHandleEvent_ToolExecutionStart(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc1",
			ToolName:   "read",
			Args:       map[string]any{"path": "foo.go"},
		},
	})

	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	// Verify args were stored in pendingArgs
	v, ok := entry.pendingArgs.Load("tc1")
	if !ok {
		t.Fatal("expected pendingArgs to contain tc1")
	}
	args := v.(map[string]any)
	if args["path"] != "foo.go" {
		t.Errorf("args[path] = %v, want foo.go", args["path"])
	}
}

func TestHandleEvent_ToolExecutionEnd(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	// Store pending args first
	entry.pendingArgs.Store("tc1", map[string]any{"path": "foo.go"})

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc1",
			ToolName:   "read",
			Result: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "file contents"},
				},
			},
			IsError: false,
		},
	})

	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}

	// Verify pendingArgs was cleaned up
	if _, ok := entry.pendingArgs.Load("tc1"); ok {
		t.Error("pendingArgs should have been deleted for tc1")
	}
}

func TestHandleEvent_ToolExecutionEnd_WithAcpTerminal(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	// Simulate a pending ACP terminal
	entry.termState.pendingBashTerminals["tc1"] = "term-1"

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc1",
			ToolName:   "bash",
			Result:     map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "output"}}},
			IsError:    false,
		},
	})

	// Terminal should be cleaned up from pendingBashTerminals
	entry.termState.mu.Lock()
	_, exists := entry.termState.pendingBashTerminals["tc1"]
	entry.termState.mu.Unlock()
	if exists {
		t.Error("pendingBashTerminals should have been cleaned up for tc1")
	}
}

func TestHandleEvent_ToolExecutionEnd_Error(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc2",
			ToolName:   "bash",
			IsError:    true,
		},
	})

	// Should still produce an update (with failed status)
	if len(mc.getUpdates()) != 1 {
		t.Fatal("expected 1 update for error tool execution end")
	}
}

func TestHandleSlashCommand_NameEmpty(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState()}

	// /name with no args should send usage message
	if !pa.handleSlashCommand("s1", entry, "name", "") {
		t.Error("expected true for /name command")
	}
	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
}

func TestBuiltInCommands_IncludesLoginChangelog(t *testing.T) {
	cmds := builtInCommands()
	names := make(map[string]bool)
	for _, cmd := range cmds {
		names[cmd.Name] = true
	}
	for _, required := range []string{"login", "changelog", "logout", "compact", "reload"} {
		if !names[required] {
			t.Errorf("missing required command: %q", required)
		}
	}
}

func TestListSessions_EmptyCwd(t *testing.T) {
	pa := &piAgent{sessions: make(map[string]*piSession)}
	resp, err := pa.ListSessions(context.Background(), ListSessionsRequest{})
	// Should not panic; may return empty or error depending on env.
	_ = resp
	_ = err
}

func TestResumeSession_InvalidPath(t *testing.T) {
	pa := &piAgent{sessions: make(map[string]*piSession)}
	_, err := pa.ResumeSession(context.Background(), ResumeSessionRequest{
		SessionId: "/tmp/../../../etc/passwd",
	})
	if err == nil {
		t.Error("expected error for path traversal attempt")
	}
}

func TestHandleSlashCommand_Changelog(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState(), session: &core.AgentSession{}}
	// Should return true and send a message (even if changelog is empty).
	result := pa.handleSlashCommand("s1", entry, "changelog", "")
	if !result {
		t.Error("expected handleSlashCommand to return true for changelog")
	}
	if len(mc.getUpdates()) == 0 {
		t.Error("expected at least one update for changelog command")
	}
}

func TestHandleSlashCommand_Login_NoArgs(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	auth := core.NewAuthStorage("")
	mr := core.NewModelRegistry(auth, "")
	entry := &piSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}
	result := pa.handleSlashCommand("s1", entry, "login", "")
	if !result {
		t.Error("expected handleSlashCommand to return true for login")
	}
	// Should have sent a message listing providers (or "no OAuth providers" message).
	if len(mc.getUpdates()) == 0 {
		t.Error("expected at least one update for login command")
	}
}

func TestHandleSlashCommand_Logout_InvalidProviderID(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	auth := core.NewAuthStorage("")
	mr := core.NewModelRegistry(auth, "")
	// Set up a fake logged-in provider
	auth.SetRuntimeApiKey("anthropic", "test-key")
	entry := &piSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}
	// Provider ID with invalid chars should be rejected
	result := pa.handleSlashCommand("s1", entry, "logout", "anthropic; rm -rf /")
	if !result {
		t.Error("expected handleSlashCommand to return true")
	}
	updates := mc.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one update")
	}
	// Should reject the invalid provider ID
	lastUpdate := updates[len(updates)-1]
	if lastUpdate.Update.AgentMessageChunk == nil {
		t.Error("expected agent message chunk update")
	}
}

func TestRawConnMethodHandler_UnknownMethod(t *testing.T) {
	pa := &piAgent{sessions: make(map[string]*piSession)}
	handler := rawMethodHandler(pa)
	result, reqErr := handler(context.Background(), "unknown/method", []byte("{}"))
	if result != nil {
		t.Errorf("expected nil result for unknown method, got %v", result)
	}
	if reqErr == nil {
		t.Error("expected MethodNotFound error for unknown method")
	}
}
