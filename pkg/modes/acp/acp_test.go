package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

	// Should not panic
	pa.handleEvent("s1", entry, core.AgentSessionEvent{AgentEvent: nil})
	if len(mc.getUpdates()) != 0 {
		t.Error("expected no updates for nil AgentEvent")
	}
}

func TestHandleEvent_ToolExecutionStart(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}

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
	oldSession := &firSession{
		session:   &core.AgentSession{},
		termState: newTerminalState(),
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
	entry := &firSession{termState: newTerminalState(), session: &core.AgentSession{}}
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
	auth := core.NewInMemoryAuthStorage(nil)
	mr := core.NewModelRegistry(auth, "")
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
	auth := core.NewInMemoryAuthStorage(nil)
	mr := core.NewModelRegistry(auth, "")
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
	handler := rawMethodHandler(pa)
	result, reqErr := handler(context.Background(), "unknown/method", []byte("{}"))
	if result != nil {
		t.Errorf("expected nil result for unknown method, got %v", result)
	}
	if reqErr == nil {
		t.Error("expected MethodNotFound error for unknown method")
	}
}

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
	entry := &firSession{termState: newTerminalState(), session: sess}

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

	handler := rawMethodHandler(pa)

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
	handler := rawMethodHandler(pa)

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

	// Create a SessionManager and populate it with messages.
	sm := core.NewSessionManager(tmpDir, sessionDir)

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

	// Create a mock agent session with the SessionManager.
	entry := &firSession{
		session:   &core.AgentSession{SessionManager: sm},
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
