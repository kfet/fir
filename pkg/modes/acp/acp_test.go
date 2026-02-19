package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/extension"
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

func TestResumeSession_DuplicateIDCleansUpOldSession(t *testing.T) {
	// Set TAU_AGENT_DIR so ResumeSession resolves the sessions directory
	// to a temp directory we control.
	agentDir := t.TempDir()
	sessionsDir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAU_AGENT_DIR", agentDir)

	// Create a fake session file inside the sessions dir.
	sessionPath := filepath.Join(sessionsDir, "my-session.json")
	if err := os.WriteFile(sessionPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Track whether the old session's unsubscribe was called.
	unsubscribeCalled := false
	oldSession := &piSession{
		session:   &core.AgentSession{},
		termState: newTerminalState(),
		unsubscribe: func() {
			unsubscribeCalled = true
		},
	}

	mc := newMockConn()
	pa := &piAgent{
		conn:     mc,
		sessions: make(map[string]*piSession),
	}
	pa.sessions[sessionPath] = oldSession

	// ResumeSession with the same ID should clean up oldSession before
	// attempting to create a new one. createSession will fail (no real LLM),
	// but cleanup must still have happened.
	_, _ = pa.ResumeSession(context.Background(), ResumeSessionRequest{
		SessionId: sessionPath,
	})

	if !unsubscribeCalled {
		t.Error("existing session's unsubscribe was not called on duplicate resume")
	}
	// The old session should no longer be in the map regardless of whether
	// createSession succeeded.
	pa.mu.Lock()
	_, stillPresent := pa.sessions[sessionPath]
	pa.mu.Unlock()
	if stillPresent && pa.sessions[sessionPath] == oldSession {
		t.Error("old session was not replaced in sessions map")
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
	auth := core.NewInMemoryAuthStorage(nil)
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
	auth := core.NewInMemoryAuthStorage(nil)
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

func TestHandleSlashCommand_ExtensionExecuteCommand(t *testing.T) {
	// Register a temporary extension command, then verify handleSlashCommand
	// calls ExecuteCommand (not session.Prompt) for it.
	extension.ClearRegistry()
	defer extension.ClearRegistry()

	var capturedArgs string
	extension.Register("acp-ext-test", func(api extension.API) {
		api.RegisterCommand("myextcmd", extension.Command{
			Description: "test command",
			Handler: func(args string, ctx extension.CommandContext) error {
				capturedArgs = args
				return nil
			},
		})
	})

	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{
		termState:       newTerminalState(),
		extensionRunner: runner,
	}

	found := pa.handleSlashCommand("s1", entry, "myextcmd", "hello world")
	if !found {
		t.Error("expected handleSlashCommand to return true for extension command")
	}
	if capturedArgs != "hello world" {
		t.Errorf("ExecuteCommand args = %q, want %q", capturedArgs, "hello world")
	}
	// Should NOT have sent any agent message (command was handled by extension, not AI)
	if len(mc.getUpdates()) != 0 {
		t.Errorf("expected no agent updates for successful extension command, got %d", len(mc.getUpdates()))
	}
}

func TestHandleSlashCommand_ExtensionExecuteCommand_Error(t *testing.T) {
	extension.ClearRegistry()
	defer extension.ClearRegistry()

	extension.Register("acp-ext-err-test", func(api extension.API) {
		api.RegisterCommand("failcmd", extension.Command{
			Description: "failing command",
			Handler: func(args string, ctx extension.CommandContext) error {
				return fmt.Errorf("intentional error")
			},
		})
	})

	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{
		termState:       newTerminalState(),
		extensionRunner: runner,
	}

	found := pa.handleSlashCommand("s1", entry, "failcmd", "")
	if !found {
		t.Error("expected handleSlashCommand to return true for registered extension command")
	}
	// Should have sent an error message to the agent
	updates := mc.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one agent message for extension command error")
	}
	if updates[0].Update.AgentMessageChunk == nil {
		t.Error("expected AgentMessageChunk for error update")
	}
}

// newMinimalSession creates a minimal AgentSession with SessionManager and Agent,
// for use in slash command tests that don't need a real LLM provider.
func newMinimalSession(t *testing.T) *core.AgentSession {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := core.NewSessionManager(cwd, filepath.Join(agentDir, "sessions"))
	rl := core.NewResourceLoader(core.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	a := agent.NewAgent(agent.AgentOptions{})
	return core.NewAgentSession(core.AgentSessionOptions{
		Agent:          a,
		SessionManager: sm,
		ResourceLoader: rl,
		Cwd:            cwd,
	})
}

func TestHandleSlashCommand_Name_WithArgs(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &piSession{termState: newTerminalState(), session: sess}

	found := pa.handleSlashCommand("s1", entry, "name", "my-session")
	if !found {
		t.Error("expected handleSlashCommand to return true for /name")
	}
	updates := mc.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one update")
	}
	chunk := updates[0].Update.AgentMessageChunk
	if chunk == nil {
		t.Fatal("expected AgentMessageChunk")
	}
	if chunk.Content.Text == nil {
		t.Fatal("expected text content block")
	}
	if !strings.Contains(chunk.Content.Text.Text, "my-session") {
		t.Errorf("expected confirmation message to contain session name, got: %q", chunk.Content.Text.Text)
	}
}

func TestHandleSlashCommand_Session(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &piSession{termState: newTerminalState(), session: sess}

	found := pa.handleSlashCommand("s1", entry, "session", "")
	if !found {
		t.Error("expected handleSlashCommand to return true for /session")
	}
	updates := mc.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one update")
	}
	chunk := updates[0].Update.AgentMessageChunk
	if chunk == nil {
		t.Fatal("expected AgentMessageChunk")
	}
	if chunk.Content.Text == nil {
		t.Fatal("expected text content block")
	}
	if !strings.Contains(chunk.Content.Text.Text, "Session Info") {
		t.Errorf("expected session info content, got: %q", chunk.Content.Text.Text)
	}
	if !strings.Contains(chunk.Content.Text.Text, "Messages") || !strings.Contains(chunk.Content.Text.Text, "Tokens") {
		t.Errorf("expected Messages and Tokens sections in session info, got: %q", chunk.Content.Text.Text)
	}
}

// getLastAgentMessage returns the text from the last AgentMessageChunk update.
func getLastAgentMessage(updates []acpsdk.SessionNotification) string {
	for i := len(updates) - 1; i >= 0; i-- {
		if c := updates[i].Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
			return c.Content.Text.Text
		}
	}
	return ""
}

func TestHandleResumeArg_InvalidNumber_NoList(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState(), agentDir: t.TempDir()}

	pa.handleResumeArg("s1", entry, "5")

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Run /resume first") {
		t.Errorf("expected 'Run /resume first' hint, got: %q", msg)
	}
}

func TestHandleResumeArg_InvalidNumber_WithList(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	agentDir := t.TempDir()
	entry := &piSession{
		termState: newTerminalState(),
		agentDir:  agentDir,
		lastResumeList: []core.SessionListInfo{
			{Path: filepath.Join(agentDir, "sessions", "a.json")},
		},
	}

	pa.handleResumeArg("s1", entry, "9")

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Pick 1-1") {
		t.Errorf("expected 'Pick 1-1' hint, got: %q", msg)
	}
}

func TestHandleResumeArg_PathOutsideSessionsDir(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	entry := &piSession{termState: newTerminalState(), agentDir: t.TempDir()}

	// Absolute path outside sessions dir
	pa.handleResumeArg("s1", entry, "/etc/passwd")

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Invalid session path") {
		t.Errorf("expected 'Invalid session path' error, got: %q", msg)
	}
}

func TestHandleResumeArg_ValidNumberFromList(t *testing.T) {
	mc := newMockConn()
	pa := &piAgent{conn: mc, sessions: make(map[string]*piSession)}
	agentDir := t.TempDir()
	// Put the session path inside the sessions dir
	sessPath := filepath.Join(agentDir, "sessions", "sess.json")
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &piSession{
		termState: newTerminalState(),
		agentDir:  agentDir,
		session:   sess,
		lastResumeList: []core.SessionListInfo{
			{Path: sessPath},
		},
	}

	// Attempting resume by number — SwitchSession will fail (no real session file),
	// but we verify path was resolved and error message was sent (not "Invalid number" message).
	pa.handleResumeArg("s1", entry, "1")

	msg := getLastAgentMessage(mc.getUpdates())
	// Should NOT be "Invalid session number" or "Run /resume first"
	if strings.Contains(msg, "Invalid session number") || strings.Contains(msg, "Run /resume") {
		t.Errorf("expected a session-switch attempt, got: %q", msg)
	}
	// Should be "Failed to resume session" or "Resumed session" (depending on file existence)
	if !strings.Contains(msg, "session") {
		t.Errorf("expected session-related message, got: %q", msg)
	}
}
