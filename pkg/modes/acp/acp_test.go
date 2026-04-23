package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

func TestPiAgent_Initialize(t *testing.T) {
	pa := &firAgent{
		sessions: make(map[string]*firSession),
	}

	resp, err := pa.Initialize(context.Background(), acpsdk.InitializeRequest{
		ClientCapabilities: acpsdk.ClientCapabilities{
			Terminal: true,
		},
	})
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "fir" {
		t.Errorf("agent name = %v, want fir", resp.AgentInfo)
	}
	if resp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		t.Errorf("protocol version = %d, want %d", resp.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}

	// Verify capabilities were stored
	if !pa.clientCaps.Terminal {
		t.Error("clientCapabilities.Terminal should be true")
	}

	// Verify authStorage was set globally
	if pa.authStorage == nil {
		t.Error("authStorage should be set after Initialize")
	}
}

func TestCreateSession_ReusesGlobalAuthStorage(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)

	globalAuth := auth.NewInMemoryAuthStorage(nil)
	mc := newMockConn()
	pa := &firAgent{
		conn:        mc,
		sessions:    make(map[string]*firSession),
		authStorage: globalAuth,
	}

	// createSession will fail (no model configured), but we can check via
	// the error path that it attempted to use the global authStorage by
	// verifying the session map wasn't populated (the error is expected).
	// More importantly, we set a runtime key on globalAuth and verify a
	// session created afterward sees it.
	globalAuth.SetRuntimeApiKey("test-provider", "test-key-123")

	// createSession will fail downstream, but the authStorage passed to
	// the model registry should be the same object.
	_, _ = pa.createSession(context.Background(), "s1", t.TempDir(), nil)

	// If a session was created (may fail for other reasons), verify it
	// shares the same authStorage.
	pa.mu.Lock()
	entry, ok := pa.sessions["s1"]
	pa.mu.Unlock()
	if ok {
		key := entry.modelRegistry.AuthStorage().GetApiKey("test-provider")
		if key != "test-key-123" {
			t.Errorf("session did not inherit global authStorage; got key %q", key)
		}
	}
}

func TestPiAgent_SetSessionModel_NotFound(t *testing.T) {
	pa := &firAgent{
		sessions: make(map[string]*firSession),
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
	pa := &firAgent{
		sessions: make(map[string]*firSession),
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	// useClientTerminal=true, useClientFs=false → 8 tools (with bash_output + bash_kill)
	toolList := pa.createAcpTools("/tmp/test", "session-1", true, false, "")

	if len(toolList) != 8 {
		t.Fatalf("expected 8 ACP tools, got %d", len(toolList))
	}

	names := make(map[string]bool)
	for _, tool := range toolList {
		names[tool.Tool.Name] = true
	}

	expected := []string{"read", "bash", "edit", "write", "grep", "find", "bash_output", "bash_kill"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %q", name)
		}
	}

	// useClientTerminal=false, useClientFs=false → 6 tools (no bash_output/bash_kill, default bash)
	toolList2 := pa.createAcpTools("/tmp/test", "session-1", false, false, "")
	if len(toolList2) != 6 {
		t.Fatalf("expected 6 ACP tools (no terminal), got %d", len(toolList2))
	}

	// useClientTerminal=false, useClientFs=true → 6 tools with ACP read/write/edit
	toolList3 := pa.createAcpTools("/tmp/test", "session-1", false, true, "")
	if len(toolList3) != 6 {
		t.Fatalf("expected 6 ACP tools (fs only), got %d", len(toolList3))
	}
}

func TestHandleEvent_TextDelta(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Should not panic
	pa.handleEvent("s1", entry, session.AgentSessionEvent{AgentEvent: nil})
	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for nil AgentEvent")
	}
}

func TestHandleEvent_ToolExecutionStart(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Store pending args first
	entry.pendingArgs.Store("tc1", map[string]any{"path": "foo.go"})

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Simulate a pending ACP terminal
	entry.termState.pendingBashTerminals["tc1"] = "term-1"

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
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

func TestHandleEvent_MessageEnd_InferenceError(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Simulate the errorAssistantMessage produced when Bedrock (or any provider) fails.
	errMsg := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		StopReason:   ai.StopReasonError,
		ErrorMessage: "bedrock: throttling exception (429)",
	}))

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageEnd,
			Message: &errMsg,
		},
	})

	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update for inference error, got %d", len(updates))
	}
	// The update must carry the error text.
	raw, _ := json.Marshal(updates[0])
	if !strings.Contains(string(raw), "bedrock: throttling exception") {
		t.Errorf("expected error text in update, got %s", raw)
	}
}

func TestHandleEvent_MessageEnd_Aborted_Silent(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Aborted (user cancel) should not produce any update — it's noisy and expected.
	abortedMsg := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		StopReason:   ai.StopReasonAborted,
		ErrorMessage: "Request was aborted",
	}))

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageEnd,
			Message: &abortedMsg,
		},
	})

	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for aborted message (user cancel)")
	}
}

func TestHandleEvent_MessageEnd_NoError_Silent(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// A normal successful message end (no ErrorMessage) should not produce an extra update.
	okMsg := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:       "assistant",
		Content:    []ai.AssistantContent{{Text: &ai.TextContent{Text: "done"}}},
		StopReason: ai.StopReasonStop,
	}))

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageEnd,
			Message: &okMsg,
		},
	})

	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for successful message end")
	}
}

func TestHandleEvent_AutoCompactionEnd_Error(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		Type:         "auto_compaction_end",
		ErrorMessage: "compaction API error (503)",
	})

	updates := mc.getUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update for auto_compaction_end error, got %d", len(updates))
	}
	raw, _ := json.Marshal(updates[0])
	if !strings.Contains(string(raw), "compaction API error") {
		t.Errorf("expected error text in update, got %s", raw)
	}
}

func TestHandleEvent_AutoCompactionEnd_Success_Silent(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Successful compaction should produce no user-visible message in ACP mode.
	pa.handleEvent("s1", entry, session.AgentSessionEvent{
		Type:             "auto_compaction_end",
		CompactionResult: &session.CompactionResultInfo{TokensBefore: 50000},
	})

	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for successful auto_compaction_end")
	}
}

func TestHandleSlashCommand_NameEmpty(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{sessions: make(map[string]*firSession)}
	resp, err := pa.ListSessions(context.Background(), ListSessionsRequest{})
	// Should not panic; may return empty or error depending on env.
	_ = resp
	_ = err
}

func TestListSessions_AllDirs(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)

	// Create two project session dirs with one session file each.
	for _, name := range []string{"--project-a--", "--project-b--"} {
		dir := filepath.Join(agentDir, "sessions", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create a minimal session file.
		f := filepath.Join(dir, "2026-01-01T00-00-00Z_test-id.jsonl")
		if err := os.WriteFile(f, []byte(`{"type":"session","version":1,"id":"test","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pa := &firAgent{sessions: make(map[string]*firSession)}
	resp, err := pa.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 sessions from 2 dirs, got %d", len(resp.Sessions))
	}
}

func TestResumeSession_InvalidPath(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	_, err := pa.ResumeSession(context.Background(), ResumeSessionRequest{
		SessionId: "/tmp/../../../etc/passwd",
	})
	if err == nil {
		t.Error("expected error for path traversal attempt")
	}
}

func TestResumeSession_DuplicateIDCleansUpOldSession(t *testing.T) {
	// Set FIR_AGENT_DIR so ResumeSession resolves the sessions directory
	// to a temp directory we control.
	agentDir := t.TempDir()
	sessionsDir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIR_AGENT_DIR", agentDir)

	// Create a fake session file inside the sessions dir.
	sessionPath := filepath.Join(sessionsDir, "my-session.json")
	if err := os.WriteFile(sessionPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Track whether the old session's unsubscribe was called.
	unsubscribeCalled := false
	mcpMgr := mcp.NewManager(nil, false)
	oldSession := &firSession{
		session:    &session.AgentSession{},
		termState:  newTerminalState(),
		mcpManager: mcpMgr,
		unsubscribe: func() {
			unsubscribeCalled = true
		},
	}

	mc := newMockConn()
	pa := &firAgent{
		conn:     mc,
		sessions: make(map[string]*firSession),
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState(), session: &session.AgentSession{}}
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	auth := auth.NewInMemoryAuthStorage(nil)
	mr := models.NewModelRegistry(auth, "")
	entry := &firSession{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	auth := auth.NewInMemoryAuthStorage(nil)
	mr := models.NewModelRegistry(auth, "")
	// Set up a fake logged-in provider
	auth.SetRuntimeApiKey("anthropic", "test-key")
	entry := &firSession{
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
	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	result, reqErr := handler(context.Background(), "unknown/method", []byte("{}"))
	if result != nil {
		t.Errorf("expected nil result for unknown method, got %v", result)
	}
	if reqErr == nil {
		t.Error("expected MethodNotFound error for unknown method")
	}
}

// for use in slash command tests that don't need a real LLM provider.
func newMinimalSession(t *testing.T) *session.AgentSession {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions"))
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	a := agent.NewAgent(agent.AgentOptions{})
	return session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   sm,
		ResourceLoader: rl,
		Cwd:            cwd,
	})
}

func TestHandleSlashCommand_Name_WithArgs(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	mgr := extension.NewManager(slog.Default())
	mgr.AllowedNames = []string{"tmuxspinner", "demo"}
	model := &ai.Model{
		ID:            "test-model",
		Provider:      ai.ProviderAnthropic,
		ContextWindow: 10000,
	}
	sess.SetModel(model)
	entry := &firSession{
		termState:       newTerminalState(),
		session:         sess,
		extSetup:        &extension.SetupResult{Manager: mgr},
		settingsManager: nil,
	}

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
	if !strings.Contains(chunk.Content.Text.Text, "**Extensions:** demo, tmuxspinner") {
		t.Errorf("expected enabled extensions in session info, got: %q", chunk.Content.Text.Text)
	}
	if !strings.Contains(chunk.Content.Text.Text, "**Context:**") {
		t.Errorf("expected context usage in session info, got: %q", chunk.Content.Text.Text)
	}
	if !strings.Contains(chunk.Content.Text.Text, "(off)") {
		t.Errorf("expected default context mode to be off in session info, got: %q", chunk.Content.Text.Text)
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState(), agentDir: t.TempDir()}

	pa.handleResumeArg("s1", entry, "5")

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Run /resume first") {
		t.Errorf("expected 'Run /resume first' hint, got: %q", msg)
	}
}

func TestHandleResumeArg_InvalidNumber_WithList(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	agentDir := t.TempDir()
	entry := &firSession{
		termState: newTerminalState(),
		agentDir:  agentDir,
		lastResumeList: []store.SessionListInfo{
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState(), agentDir: t.TempDir()}

	// Absolute path outside sessions dir
	pa.handleResumeArg("s1", entry, "/etc/passwd")

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Invalid session path") {
		t.Errorf("expected 'Invalid session path' error, got: %q", msg)
	}
}

func TestHandleResumeArg_ValidNumberFromList(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	agentDir := t.TempDir()
	// Put the session path inside the sessions dir
	sessPath := filepath.Join(agentDir, "sessions", "sess.json")
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{
		termState: newTerminalState(),
		agentDir:  agentDir,
		session:   sess,
		lastResumeList: []store.SessionListInfo{
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

// ============================================================================
// /share and /expose tests
// ============================================================================

func TestBuiltInCommands_IncludesShareAndExpose(t *testing.T) {
	cmds := builtInCommands()
	names := make(map[string]bool)
	for _, cmd := range cmds {
		names[cmd.Name] = true
	}
	for _, required := range []string{"share", "export"} {
		if !names[required] {
			t.Errorf("missing required command: %q", required)
		}
	}
}

func TestHandleSlashCommand_Share_Recognized(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

	// /share is always recognized (returns true) even if gh is unavailable.
	// We can't synchronously observe the goroutine result, but we can verify
	// it doesn't return false (unhandled).
	result := pa.handleSlashCommand("s1", entry, "share", "")
	if !result {
		t.Error("expected handleSlashCommand to return true for /share")
	}
}

func TestHandleSlashCommand_Export_Recognized(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

	result := pa.handleSlashCommand("s1", entry, "export", "")
	if !result {
		t.Error("expected handleSlashCommand to return true for /export")
	}
}

func TestHandleSlashCommand_Export_WritesFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.html")

	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

	// /export with an explicit output path — the goroutine is short-lived;
	// wait up to 2 seconds for the message to arrive.
	result := pa.handleSlashCommand("s1", entry, "export", outPath)
	if !result {
		t.Error("expected handleSlashCommand to return true for /export")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg := getLastAgentMessage(mc.getUpdates())
		if strings.Contains(msg, "exported to") {
			// File should exist.
			if _, err := os.Stat(outPath); err != nil {
				t.Errorf("expected output file to exist at %s: %v", outPath, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("timed out waiting for export message; last msg: %q", getLastAgentMessage(mc.getUpdates()))
}

func TestGistIDRegex(t *testing.T) {
	valid := []string{
		"d168778e8e62f65886000f3f314d63e3",
		"AABBCCDDEEFF00112233445566778899",
		"abcdef0123456789abcdef0123456789",
	}
	invalid := []string{
		"",
		"short",
		"not-hex-string!!!!!!!!",
		"d168778e8e62f6588600", // exactly 20 chars — valid
	}
	for _, s := range valid {
		if !gistIDRegex.MatchString(s) {
			t.Errorf("gistIDRegex should match %q", s)
		}
	}
	// The 19-char case is invalid (< 20).
	if gistIDRegex.MatchString("d168778e8e62f658860") {
		t.Error("gistIDRegex should NOT match a 19-char hex string")
	}
	for _, s := range invalid[:3] {
		if gistIDRegex.MatchString(s) {
			t.Errorf("gistIDRegex should NOT match %q", s)
		}
	}
}

func TestIsNotFound_ExitError(t *testing.T) {
	// An ExitError (non-zero exit) means the command exists but failed — NOT "not found".
	// Simulate this by running a command that exits non-zero.
	cmd := exec.Command("false")
	err := cmd.Run()
	if err == nil {
		t.Skip("'false' command succeeded unexpectedly")
	}
	if isNotFound(err) {
		t.Error("isNotFound should return false for *exec.ExitError (command exists but failed)")
	}
}

func TestIsNotFound_CommandNotFound(t *testing.T) {
	// A command that doesn't exist at all returns a non-ExitError.
	cmd := exec.Command("this-command-does-not-exist-fir-test-12345")
	err := cmd.Run()
	if err == nil {
		t.Skip("unexpected success")
	}
	if !isNotFound(err) {
		t.Error("isNotFound should return true when command is not on PATH")
	}
}

func TestPerformShare_GhNotAuthenticated_SendsError(t *testing.T) {
	// Only run when gh is installed but we can override PATH to simulate failure.
	// We simulate "gh auth status" failing by running performShare against a fake
	// gh that exits 1 (not found = false, auth failure = true).
	// We do this by putting a wrapper script first on PATH.
	fakeGhDir := t.TempDir()
	fakeGhScript := filepath.Join(fakeGhDir, "gh")
	if err := os.WriteFile(fakeGhScript, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeGhDir+":"+origPath)

	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

	pa.performShare("s1", entry)

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "not logged in") && !strings.Contains(msg, "not installed") {
		t.Errorf("expected gh auth error message, got: %q", msg)
	}
}

func TestPerformShare_GhNotInstalled_SendsError(t *testing.T) {
	// Override PATH so gh is not found at all.
	t.Setenv("PATH", t.TempDir())

	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess}

	pa.performShare("s1", entry)

	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "not installed") && !strings.Contains(msg, "not logged in") {
		t.Errorf("expected 'not installed' error message, got: %q", msg)
	}
}

// ============================================================================
// Task 6: ACP injection — NewSessionRequestExt / mcpServers tests
// ============================================================================

func TestNewSessionRequestExt_JSONUnmarshal(t *testing.T) {
	const raw = `{
		"cwd": "/tmp/proj",
		"mcpServers": [
			{
				"name": "myserver",
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": [
					{"name": "NODE_PATH", "value": "/usr/local/lib/node_modules"}
				]
			}
		]
	}`

	var ext acpsdk.NewSessionRequest
	if err := json.Unmarshal([]byte(raw), &ext); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if ext.Cwd != "/tmp/proj" {
		t.Errorf("Cwd = %q, want /tmp/proj", ext.Cwd)
	}
	if len(ext.McpServers) != 1 {
		t.Fatalf("McpServers len = %d, want 1", len(ext.McpServers))
	}
	srv := ext.McpServers[0]
	if srv.Stdio.Command != "npx" {
		t.Errorf("Command = %q, want npx", srv.Stdio.Command)
	}
	if len(srv.Stdio.Args) != 3 || srv.Stdio.Args[0] != "-y" {
		t.Errorf("Args = %v, want [-y ...]", srv.Stdio.Args)
	}
	if srv.Stdio.Env[0].Name != "NODE_PATH" || srv.Stdio.Env[0].Value != "/usr/local/lib/node_modules" {
		t.Errorf("Env[0] = %q", srv.Stdio.Env[0])
	}
}

func TestNewSessionRequestExt_EmptyMcpServers(t *testing.T) {
	const raw = `{"cwd": "/tmp"}`
	var ext acpsdk.NewSessionRequest
	if err := json.Unmarshal([]byte(raw), &ext); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if len(ext.McpServers) != 0 {
		t.Errorf("expected empty McpServers, got %d entries", len(ext.McpServers))
	}
}

// TestRawConnMethodHandler_SessionNew_AcceptsMcpServers verifies that the
// "session/new" handler correctly parses a payload that includes mcpServers
// without a JSON parse error (createSession will fail due to no model or a
// fast-exiting MCP server, but the error must NOT be an InvalidParams/-32602).
func TestRawConnMethodHandler_SessionNew_AcceptsMcpServers(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}

	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))

	// Use "false" (exits immediately) as the MCP server command.  This keeps
	// the test fast: the MCP handshake fails with EOF right away instead of
	// waiting on a real external process.  We only care that the JSON parses
	// correctly (not that an MCP server actually starts).
	payload := `{
		"cwd": "/tmp",
		"mcpServers": [
			{
				"name": "dummy",
				"command": "false",
				"args": [],
				"env": []
			}
		]
	}`

	// A short timeout prevents any SDK-level retry from hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, reqErr := handler(ctx, "session/new", []byte(payload))
	// InvalidParams (-32602) means JSON was rejected — that's the failure mode.
	// Any other outcome (nil error or a different error code) is acceptable.
	if reqErr != nil && reqErr.Code == -32602 {
		t.Errorf("unexpected InvalidParams error (JSON parse rejected mcpServers?): %+v", reqErr)
	}
}

// TestRawConnMethodHandler_SessionNew_NonStdioMCPServerSkipped verifies that
// an HTTP-transport MCP server entry in mcpServers does not panic (nil Stdio
// dereference) and is silently skipped.
func TestRawConnMethodHandler_SessionNew_NonStdioMCPServerSkipped(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))

	// HTTP-transport server — Stdio will be nil after SDK unmarshal.
	// The "type":"http" discriminator is required for the SDK to pick the Http variant.
	payload := `{
		"cwd": "/tmp",
		"mcpServers": [
			{"type":"http","name":"remote-srv","url":"http://localhost:9999","headers":[]}
		]
	}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Must not panic. Any non-InvalidParams outcome is fine.
	_, reqErr := handler(ctx, "session/new", []byte(payload))
	if reqErr != nil && reqErr.Code == -32602 {
		t.Errorf("unexpected InvalidParams: %+v", reqErr)
	}
}

func TestReplaySessionHistory(t *testing.T) {
	// Create a temp dir for session storage.
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a SessionStore and populate it with messages.
	sm := store.NewSessionStore(tmpDir, sessionDir)

	// User message
	sm.AppendAIMessage(ai.NewUserMsg("Hello, how are you?", time.Now().UnixMilli()))

	// Assistant message with text and a tool call
	sm.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewTextContent("Let me check that for you."),
			ai.NewToolCallContent("tc-1", "bash", map[string]any{"command": "echo hello"}),
		},
	}))

	// Tool result
	sm.AppendAIMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		Role:       "toolResult",
		ToolCallID: "tc-1",
		ToolName:   "bash",
		Content:    []ai.ToolResultContent{{Type: "text", Text: "hello\n"}},
	}))

	// Final assistant message
	sm.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:    "assistant",
		Content: []ai.AssistantContent{ai.NewTextContent("The command executed successfully.")},
	}))

	// Create a mock agent session with the SessionStore.
	entry := &firSession{
		session:   &session.AgentSession{SessionStore: sm},
		termState: newTerminalState(),
	}

	mc := newMockConn()
	pa := &firAgent{
		conn:     mc,
		sessions: map[string]*firSession{"test-session": entry},
	}

	pa.replaySessionHistory("test-session", entry)

	mc.mu.Lock()
	updates := make([]acpsdk.SessionNotification, len(mc.updates))
	copy(updates, mc.updates)
	mc.mu.Unlock()

	// We expect: user msg, agent text, tool call start, tool result, agent text
	// = 5 updates
	if len(updates) < 5 {
		t.Fatalf("expected at least 5 updates, got %d", len(updates))
	}

	// First update: user message
	if updates[0].Update.UserMessageChunk == nil {
		t.Error("expected first update to be user message chunk")
	} else if updates[0].Update.UserMessageChunk.Content.Text == nil ||
		updates[0].Update.UserMessageChunk.Content.Text.Text != "Hello, how are you?" {
		t.Error("user message text mismatch")
	}

	// Second: agent text
	if updates[1].Update.AgentMessageChunk == nil {
		t.Error("expected second update to be agent message chunk")
	}

	// Third: tool call start
	if updates[2].Update.ToolCall == nil {
		t.Error("expected third update to be tool call start")
	} else if string(updates[2].Update.ToolCall.ToolCallId) != "tc-1" {
		t.Errorf("tool call ID = %q, want %q", updates[2].Update.ToolCall.ToolCallId, "tc-1")
	}

	// Fourth: tool call update (result)
	if updates[3].Update.ToolCallUpdate == nil {
		t.Error("expected fourth update to be tool call update")
	} else if string(updates[3].Update.ToolCallUpdate.ToolCallId) != "tc-1" {
		t.Errorf("tool call update ID = %q, want %q", updates[3].Update.ToolCallUpdate.ToolCallId, "tc-1")
	}

	// Fifth: final agent message
	if updates[4].Update.AgentMessageChunk == nil {
		t.Error("expected fifth update to be agent message chunk")
	}
}

// ============================================================================
// Extension command tests
// ============================================================================

// noopBridgeAPI implements extension.BridgeAPI for testing — all methods are no-ops.
type noopBridgeAPI struct{}

func (n *noopBridgeAPI) Exec(_ string, _ []string) (extension.ExecResult, error) {
	return extension.ExecResult{}, nil
}
func (n *noopBridgeAPI) SendMessage(_ extension.CustomMessageSpec, _ *extension.SendMessageOptions) {}
func (n *noopBridgeAPI) SendUserMessage(_ string, _ *extension.SendUserMessageOptions)              {}
func (n *noopBridgeAPI) SetSessionName(_ string)                                                    {}
func (n *noopBridgeAPI) GetSessionName() string                                                     { return "" }
func (n *noopBridgeAPI) SetLabel(_ string, _ string)                                                {}
func (n *noopBridgeAPI) ClearLabel(_ string)                                                        {}
func (n *noopBridgeAPI) SetModel(_ *ai.Model) bool                                                  { return false }
func (n *noopBridgeAPI) ContinueSession() error                                                     { return nil }
func (n *noopBridgeAPI) SideQuery(_ string) (string, error)                                         { return "", nil }
func (n *noopBridgeAPI) RegisterTool(_ extension.ToolDefinition)                                    {}
func (n *noopBridgeAPI) SetSessionData(_, _ string)                                                 {}
func (n *noopBridgeAPI) GetSessionData(_ string) (string, bool)                                     { return "", false }
func (n *noopBridgeAPI) CallTool(_ context.Context, _ string, _ map[string]any) (extension.ToolResult, error) {
	return extension.ToolResult{}, nil
}
func (n *noopBridgeAPI) PrependContext(_ string)         {}
func (n *noopBridgeAPI) ListTools() []extension.ToolInfo { return nil }
func (n *noopBridgeAPI) ReportProgress(_ string)         {}
func (n *noopBridgeAPI) Introspect() session.Introspection {
	return session.Introspection{}
}

// writeCommandExtScript writes a Python extension script that:
//   - responds to the init handshake with a "greet" command
//   - responds to hook/command calls with a "Hello!" message
//
// Skips the test if python3 is not available.
func writeCommandExtScript(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	extDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, "greet-ext.py")
	script := `#!/usr/bin/env python3
# ---
# name: greet-ext
# description: Test greeting extension
# ---
import sys, json, io
stdin = io.TextIOWrapper(sys.stdin.buffer, encoding='utf-8')
line = stdin.readline().strip()
if line:
    req = json.loads(line)
    resp = {"jsonrpc": "2.0", "id": req.get("id", 1),
            "result": {"name": "greet-ext", "commands": [{"name": "greet", "description": "Say hello"}]}}
    print(json.dumps(resp), flush=True)
for line in stdin:
    line = line.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
        resp = {"jsonrpc": "2.0", "id": req.get("id", 1), "result": {"message": "Hello!"}}
        print(json.dumps(resp), flush=True)
    except Exception:
        pass
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

// startExtManager creates and starts an extension.Manager with the given project dir,
// returning the manager and a stop func.
func startExtManager(t *testing.T, projectDir, scriptPath string) (*extension.Manager, func()) {
	t.Helper()
	trustPath := filepath.Join(projectDir, "trust.json")
	ts := extension.NewTrustStoreWithPath(trustPath)
	hash, err := extension.ComputeHash(scriptPath)
	if err != nil {
		t.Fatal("compute hash:", err)
	}
	if err := ts.RecordTrust(projectDir, "greet-ext", hash); err != nil {
		t.Fatal("record trust:", err)
	}

	mgr := extension.NewManager(slog.Default())
	mgr.SetTrustStore(ts)
	if err := mgr.Start(context.Background(), projectDir, projectDir, &noopBridgeAPI{}); err != nil {
		t.Fatal("mgr.Start:", err)
	}

	// Poll until commands appear (extension handshake is async).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(mgr.GetCommands()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(mgr.GetCommands()) == 0 {
		_ = mgr.Stop()
		t.Fatal("extension commands did not appear within 5s")
	}

	return mgr, func() { _ = mgr.Stop() }
}

// TestSendAvailableCommands_IncludesExtensionCommands verifies that extension
// commands registered via the Python extension system appear in the
// available_commands_update notification sent after session/new.
func TestSendAvailableCommands_IncludesExtensionCommands(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeCommandExtScript(t, dir)
	mgr, stop := startExtManager(t, dir, scriptPath)
	defer stop()

	mc := newMockConn()
	extSetup := &extension.SetupResult{Manager: mgr}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess, extSetup: extSetup}
	pa := &firAgent{conn: mc, sessions: map[string]*firSession{"s1": entry}}

	pa.sendAvailableCommands("s1")

	updates := mc.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one update")
	}
	u := updates[0].Update
	if u.AvailableCommandsUpdate == nil {
		t.Fatal("expected AvailableCommandsUpdate")
	}
	var found bool
	for _, cmd := range u.AvailableCommandsUpdate.AvailableCommands {
		if cmd.Name == "greet" && cmd.Description == "Say hello" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("extension command 'greet' not in available commands; got: %+v", u.AvailableCommandsUpdate.AvailableCommands)
	}
}

// TestHandleSlashCommand_ExtensionDispatch verifies that an extension-registered
// slash command is dispatched to the extension and its response is forwarded as
// an agent message.
func TestHandleSlashCommand_ExtensionDispatch(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeCommandExtScript(t, dir)
	mgr, stop := startExtManager(t, dir, scriptPath)
	defer stop()

	mc := newMockConn()
	extSetup := &extension.SetupResult{Manager: mgr}
	entry := &firSession{termState: newTerminalState(), extSetup: extSetup}
	pa := &firAgent{conn: mc, sessions: map[string]*firSession{"s1": entry}}

	handled := pa.handleSlashCommand("s1", entry, "greet", "world")
	if !handled {
		t.Error("expected handleSlashCommand to return true for extension command")
	}

	// The extension returns {"message": "Hello!"} which should appear as an agent message.
	updates := mc.getUpdates()
	var found bool
	for _, n := range updates {
		if n.Update.AgentMessageChunk != nil && n.Update.AgentMessageChunk.Content.Text != nil {
			if strings.Contains(n.Update.AgentMessageChunk.Content.Text.Text, "Hello!") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected extension response 'Hello!' in agent messages; got %d updates", len(updates))
	}
}

// TestHandleSlashCommand_ExtensionNilGuard verifies that handleSlashCommand
// does not panic and returns false when extSetup is nil.
func TestHandleSlashCommand_ExtensionNilGuard(t *testing.T) {
	mc := newMockConn()
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess, extSetup: nil}
	pa := &firAgent{conn: mc, sessions: map[string]*firSession{"s1": entry}}

	// "myext" is not a built-in command and there's no extension, so returns false.
	handled := pa.handleSlashCommand("s1", entry, "myext", "")
	if handled {
		t.Error("expected handleSlashCommand to return false for unknown command with nil extSetup")
	}
}

// TestWriteNotifier_AfterWrite verifies that AfterWrite signals after a write.
func TestWriteNotifier_AfterWrite(t *testing.T) {
	var buf strings.Builder
	wn := newWriteNotifier(&buf)

	ch := wn.AfterWrite()

	// Channel should not be signaled yet.
	select {
	case <-ch:
		t.Fatal("AfterWrite signaled before any write")
	default:
	}

	// Write something.
	_, _ = wn.Write([]byte("hello"))

	// Channel should now be closed.
	select {
	case <-ch:
		// ok
	default:
		t.Fatal("AfterWrite not signaled after write")
	}

	if buf.String() != "hello" {
		t.Errorf("inner writer got %q, want %q", buf.String(), "hello")
	}
}

// TestSessionNew_CommandsAfterResponse verifies that the available_commands_update
// notification is sent AFTER the session/new response, not before.
// This is the test that would have caught the race where the goroutine in
// NewSession sent the notification before the response was on the wire.
func TestSessionNew_CommandsAfterResponse(t *testing.T) {
	// Use a writer that records the order of JSON messages.
	var mu sync.Mutex
	var messages []string

	pr, pw := io.Pipe()
	wn := newWriteNotifier(pw)

	// Read JSON lines from the pipe and classify them.
	go func() {
		decoder := json.NewDecoder(pr)
		for decoder.More() {
			var msg map[string]any
			if err := decoder.Decode(&msg); err != nil {
				return
			}
			mu.Lock()
			if _, hasResult := msg["result"]; hasResult {
				messages = append(messages, "response")
			} else if method, _ := msg["method"].(string); method == "session/update" {
				messages = append(messages, "notification")
			}
			mu.Unlock()
		}
	}()

	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, wn)
	conn := acpsdk.NewConnection(handler, wn, strings.NewReader(""))
	pa.conn = &rawConn{conn: conn}

	// Create a minimal session manually (NewSession requires too much infra).
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{session: sess, termState: newTerminalState()}
	pa.mu.Lock()
	pa.sessions["test-sess"] = entry
	pa.mu.Unlock()

	// Simulate the initialize + session/new flow by calling the handler directly.
	// First initialize (required to set clientCaps).
	initParams, _ := json.Marshal(acpsdk.InitializeRequest{
		ProtocolVersion:    1,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	handler(context.Background(), "initialize", initParams)

	// Now simulate session/new via the handler.
	// We can't call NewSession directly because it creates a full AgentSession.
	// Instead, test the mechanism: call sendAvailableCommands deferred via writeNotifier.
	afterWrite := wn.AfterWrite()
	go func() {
		<-afterWrite
		pa.sendAvailableCommands("test-sess")
	}()

	// Simulate the response write (this is what handleInbound does after handler returns).
	resp := map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"sessionId": "test-sess"}}
	b, _ := json.Marshal(resp)
	b = append(b, '\n')
	_, _ = wn.Write(b)

	// Give the goroutine time to send the notification.
	time.Sleep(100 * time.Millisecond)
	pw.Close()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d: %v", len(messages), messages)
	}
	if messages[0] != "response" {
		t.Errorf("first message should be response, got %q", messages[0])
	}
	if messages[1] != "notification" {
		t.Errorf("second message should be notification, got %q", messages[1])
	}
}

// ============================================================================
// Tool Execute closure tests
// ============================================================================

func TestAcpBashTool_SessionNotFound(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	tool := pa.createAcpBashTool("/tmp", "nonexistent-session", "")

	_, err := tool.Execute(context.Background(), "tc1", map[string]any{"command": "echo hi"}, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'session not found', got %q", err.Error())
	}
}

func TestAcpBashTool_ShellCommandPrefix(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}

	// Create a session so the lookup succeeds (it will still fail on AcpBashExec
	// since there's no real terminal, but we can check the command was prefixed).
	entry := &firSession{
		session:   &session.AgentSession{},
		termState: newTerminalState(),
	}
	pa.sessions["s1"] = entry

	prefix := "set -euo pipefail"
	tool := pa.createAcpBashTool("/tmp", "s1", prefix)

	// The execute will fail because there's no real ACP terminal connection,
	// but the prefix was applied before the AcpBashExec call. We verify the tool
	// was created with the right description.
	if !strings.Contains(tool.Tool.Description, "Execute a bash command") {
		t.Error("bash tool missing expected description")
	}
	if tool.Tool.Name != "bash" {
		t.Errorf("expected tool name 'bash', got %q", tool.Tool.Name)
	}
}

func TestBashOutputTool_SessionNotFound(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	tool := pa.createBashOutputTool("nonexistent-session")

	_, err := tool.Execute(context.Background(), "tc1", map[string]any{"command_id": "cmd1"}, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'session not found', got %q", err.Error())
	}
}

func TestBashKillTool_SessionNotFound(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	tool := pa.createBashKillTool("nonexistent-session")

	_, err := tool.Execute(context.Background(), "tc1", map[string]any{"command_id": "cmd1"}, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'session not found', got %q", err.Error())
	}
}

func TestAcpBashTool_BackgroundCommand_SessionNotFound(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	tool := pa.createAcpBashTool("/tmp", "nonexistent-session", "")

	_, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"command":           "sleep 100",
		"run_in_background": true,
	}, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'session not found', got %q", err.Error())
	}
}

// ============================================================================
// ACP slash command tests
// ============================================================================

func TestHandleSlashCommand_Reload(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{
		termState: newTerminalState(),
		session:   sess,
	}

	found := pa.handleSlashCommand("s1", entry, "reload", "")
	if !found {
		t.Error("expected handleSlashCommand to return true for reload")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Reload completed") {
		t.Errorf("expected reload success message, got: %q", msg)
	}
}

func TestHandleSlashCommand_SkillsList(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{
		termState: newTerminalState(),
		session:   sess,
	}

	found := pa.handleSlashCommand("s1", entry, "skills", "")
	if !found {
		t.Error("expected handleSlashCommand to return true for skills")
	}
	// Should send some message (either skill list or "No skills loaded")
	if len(mc.getUpdates()) == 0 {
		t.Error("expected at least one update for skills command")
	}
}

func TestHandleSlashCommand_SkillsInstall_Unknown(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{
		termState: newTerminalState(),
		session:   sess,
		cwd:       t.TempDir(),
	}

	found := pa.handleSlashCommand("s1", entry, "skills", "install nonexistent-skill-xyz")
	if !found {
		t.Error("expected handleSlashCommand to return true for skills install")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Unknown builtin skill") {
		t.Errorf("expected unknown skill error, got: %q", msg)
	}
}

func TestHandleSlashCommand_Logout_NoProviders(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	as := auth.NewInMemoryAuthStorage(nil)
	mr := models.NewModelRegistry(as, "")
	entry := &firSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}

	found := pa.handleSlashCommand("s1", entry, "logout", "")
	if !found {
		t.Error("expected handleSlashCommand to return true for logout")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "No providers") {
		t.Errorf("expected 'No providers' message, got: %q", msg)
	}
}

func TestHandleSlashCommand_Logout_All(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	as := auth.NewInMemoryAuthStorage(auth.AuthStorageData{"anthropic": {Type: auth.CredentialTypeAPIKey, Key: "test-key"}})
	mr := models.NewModelRegistry(as, "")
	entry := &firSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}

	found := pa.handleSlashCommand("s1", entry, "logout", "all")
	if !found {
		t.Error("expected handleSlashCommand to return true for logout all")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Logged out from all") {
		t.Errorf("expected 'Logged out from all' message, got: %q", msg)
	}
}

func TestHandleSlashCommand_Logout_SpecificProvider(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	as := auth.NewInMemoryAuthStorage(auth.AuthStorageData{"anthropic": {Type: auth.CredentialTypeAPIKey, Key: "test-key"}})
	mr := models.NewModelRegistry(as, "")
	entry := &firSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}

	found := pa.handleSlashCommand("s1", entry, "logout", "anthropic")
	if !found {
		t.Error("expected handleSlashCommand to return true for logout")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Logged out from anthropic") {
		t.Errorf("expected 'Logged out from anthropic' message, got: %q", msg)
	}
}

func TestHandleSlashCommand_Logout_NotLoggedIn(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	as := auth.NewInMemoryAuthStorage(auth.AuthStorageData{"anthropic": {Type: auth.CredentialTypeAPIKey, Key: "test-key"}})
	mr := models.NewModelRegistry(as, "")
	entry := &firSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}

	found := pa.handleSlashCommand("s1", entry, "logout", "openai")
	if !found {
		t.Error("expected handleSlashCommand to return true for logout")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "not logged in") {
		t.Errorf("expected 'not logged in' message, got: %q", msg)
	}
}

func TestHandleSlashCommand_Login_InvalidProviderID(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	as := auth.NewInMemoryAuthStorage(nil)
	mr := models.NewModelRegistry(as, "")
	entry := &firSession{
		termState:     newTerminalState(),
		modelRegistry: mr,
	}

	found := pa.handleSlashCommand("s1", entry, "login", "bad;provider")
	if !found {
		t.Error("expected handleSlashCommand to return true for login")
	}
	msg := getLastAgentMessage(mc.getUpdates())
	if !strings.Contains(msg, "Invalid provider ID") && !strings.Contains(msg, "No OAuth providers available") {
		t.Errorf("expected 'Invalid provider ID' or 'No OAuth providers available' message, got: %q", msg)
	}
}
