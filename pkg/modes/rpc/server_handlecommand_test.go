package rpc

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
)

// testSession creates a minimal AgentSession for testing RPC command dispatch.
func testSession() *core.AgentSession {
	sm := core.InMemorySessionManager("/tmp/rpc-test")
	a := agent.NewAgent(agent.AgentOptions{})
	return core.NewAgentSession(core.AgentSessionOptions{
		Agent:          a,
		SessionManager: sm,
		ResourceLoader: &noopResourceLoader{},
		Cwd:            "/tmp/rpc-test",
	})
}

// testServer creates a Server with a test session.
func testServer() *Server {
	return &Server{session: testSession()}
}

// noopResourceLoader implements core.ResourceLoader for testing.
type noopResourceLoader struct{}

func (r *noopResourceLoader) GetSkills() ([]core.Skill, []core.ResourceDiagnostic)     { return nil, nil }
func (r *noopResourceLoader) GetPrompts() ([]core.PromptTemplate, []core.ResourceDiagnostic) {
	return nil, nil
}
func (r *noopResourceLoader) GetAgentsFiles() []core.AgentsFile     { return nil }
func (r *noopResourceLoader) GetSystemPrompt() string               { return "" }
func (r *noopResourceLoader) GetAppendSystemPrompt() []string       { return nil }
func (r *noopResourceLoader) GetPathMetadata() map[string]core.PathMetadata { return nil }
func (r *noopResourceLoader) ExtendResources(core.ResourceExtensionPaths)   {}
func (r *noopResourceLoader) Reload() error                                 { return nil }

// mockResourceLoader implements core.ResourceLoader with configurable prompts and skills.
type mockResourceLoader struct {
	prompts []core.PromptTemplate
	skills  []core.Skill
}

func (r *mockResourceLoader) GetSkills() ([]core.Skill, []core.ResourceDiagnostic) {
	return r.skills, nil
}
func (r *mockResourceLoader) GetPrompts() ([]core.PromptTemplate, []core.ResourceDiagnostic) {
	return r.prompts, nil
}
func (r *mockResourceLoader) GetAgentsFiles() []core.AgentsFile     { return nil }
func (r *mockResourceLoader) GetSystemPrompt() string               { return "" }
func (r *mockResourceLoader) GetAppendSystemPrompt() []string       { return nil }
func (r *mockResourceLoader) GetPathMetadata() map[string]core.PathMetadata { return nil }
func (r *mockResourceLoader) ExtendResources(core.ResourceExtensionPaths)   {}
func (r *mockResourceLoader) Reload() error                                 { return nil }

// ---------------------------------------------------------------------------
// handleCommand tests — state/info commands
// ---------------------------------------------------------------------------

func TestHandleCommand_GetState(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "1", Type: CmdGetState})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	// Verify we get a session state back
	data, _ := json.Marshal(resp.Data)
	var state RpcSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	if state.SessionID != "default" {
		t.Errorf("expected sessionID 'default', got %q", state.SessionID)
	}
}

func TestHandleCommand_GetSessionStats(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "2", Type: CmdGetSessionStats})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	data, _ := json.Marshal(resp.Data)
	var stats SessionStats
	json.Unmarshal(data, &stats)
	if stats.TotalMessages != 0 {
		t.Errorf("expected 0 total messages, got %d", stats.TotalMessages)
	}
}

func TestHandleCommand_GetMessages(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "3", Type: CmdGetMessages})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_GetLastAssistantText_Empty(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "4", Type: CmdGetLastAssistantText})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	data, _ := json.Marshal(resp.Data)
	var result GetLastAssistantTextData
	json.Unmarshal(data, &result)
	if result.Text != nil {
		t.Error("expected nil text for empty messages")
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — model commands
// ---------------------------------------------------------------------------

func TestHandleCommand_SetModel_NoRegistry(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{
		ID:       "5",
		Type:     CmdSetModel,
		Provider: "anthropic",
		ModelID:  "claude-3",
	})
	if resp.Success {
		t.Error("expected error when model registry is nil")
	}
	if resp.Error != "model registry not available" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestHandleCommand_GetAvailableModels_NoRegistry(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "6", Type: CmdGetAvailableModels})
	if resp.Success {
		t.Error("expected error when model registry is nil")
	}
}

func TestHandleCommand_CycleModel(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "7", Type: CmdCycleModel})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — thinking/mode commands
// ---------------------------------------------------------------------------

func TestHandleCommand_SetThinkingLevel(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{
		ID:    "8",
		Type:  CmdSetThinkingLevel,
		Level: ai.ThinkingHigh,
	})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_CycleThinkingLevel(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "9", Type: CmdCycleThinkingLevel})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_SetSteeringMode(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "10", Type: CmdSetSteeringMode})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_SetFollowUpMode(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "11", Type: CmdSetFollowUpMode})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — session commands
// ---------------------------------------------------------------------------

func TestHandleCommand_NewSession(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "12", Type: CmdNewSession})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_Fork(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "13", Type: CmdFork, EntryID: "nonexistent"})
	// Should fail for nonexistent entry ID
	if resp.Success {
		t.Error("expected error for nonexistent entry ID")
	}
}

func TestHandleCommand_SetSessionName_Empty(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "14", Type: CmdSetSessionName, Name: ""})
	if resp.Success {
		t.Error("expected error for empty session name")
	}
	if resp.Error != "Session name cannot be empty" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestHandleCommand_SetSessionName_Valid(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "15", Type: CmdSetSessionName, Name: "My Session"})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — compaction/retry
// ---------------------------------------------------------------------------

func TestHandleCommand_Compact_NoRunner(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "16", Type: CmdCompact})
	if resp.Success {
		t.Error("expected error when compaction runner is nil")
	}
}

func TestHandleCommand_SetAutoCompaction(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "17", Type: CmdSetAutoCompaction})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_SetAutoCompaction_UpdatesSetting(t *testing.T) {
	sm := core.InMemorySessionManager("/tmp/rpc-test")
	a := agent.NewAgent(agent.AgentOptions{})
	settings := core.NewInMemorySettingsManager(core.Settings{})
	session := core.NewAgentSession(core.AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		ResourceLoader:  &noopResourceLoader{},
		Cwd:             "/tmp/rpc-test",
		SettingsManager: settings,
	})
	srv := &Server{session: session}

	// compaction is enabled by default
	if !settings.GetCompactionEnabled() {
		t.Fatal("expected compaction enabled by default")
	}

	enabled := false
	resp := srv.handleCommand(RpcCommand{ID: "x", Type: CmdSetAutoCompaction, Enabled: &enabled})
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if settings.GetCompactionEnabled() {
		t.Error("expected compaction disabled after set_auto_compaction false")
	}

	enabled = true
	resp = srv.handleCommand(RpcCommand{ID: "y", Type: CmdSetAutoCompaction, Enabled: &enabled})
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}
	if !settings.GetCompactionEnabled() {
		t.Error("expected compaction re-enabled after set_auto_compaction true")
	}
}

func TestHandleCommand_GetState_AutoCompactionReflectsSetting(t *testing.T) {
	sm := core.InMemorySessionManager("/tmp/rpc-test-state")
	a := agent.NewAgent(agent.AgentOptions{})
	settings := core.NewInMemorySettingsManager(core.Settings{})
	session := core.NewAgentSession(core.AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		ResourceLoader:  &noopResourceLoader{},
		Cwd:             "/tmp/rpc-test-state",
		SettingsManager: settings,
	})
	srv := &Server{session: session}

	getAutoCompaction := func() bool {
		resp := srv.handleCommand(RpcCommand{ID: "s", Type: CmdGetState})
		if !resp.Success {
			t.Fatalf("CmdGetState failed: %s", resp.Error)
		}
		data, _ := json.Marshal(resp.Data)
		var state RpcSessionState
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatalf("unmarshal state: %v", err)
		}
		return state.AutoCompactionEnabled
	}

	// Default: enabled
	if !getAutoCompaction() {
		t.Error("expected AutoCompactionEnabled=true by default")
	}

	// Disable via CmdSetAutoCompaction
	disabled := false
	srv.handleCommand(RpcCommand{ID: "d", Type: CmdSetAutoCompaction, Enabled: &disabled})
	if getAutoCompaction() {
		t.Error("expected AutoCompactionEnabled=false after disable")
	}

	// Re-enable
	enabled := true
	srv.handleCommand(RpcCommand{ID: "e", Type: CmdSetAutoCompaction, Enabled: &enabled})
	if !getAutoCompaction() {
		t.Error("expected AutoCompactionEnabled=true after re-enable")
	}
}

func TestHandleCommand_SetAutoRetry(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "18", Type: CmdSetAutoRetry})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_AbortRetry(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "19", Type: CmdAbortRetry})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — misc commands
// ---------------------------------------------------------------------------

func TestHandleCommand_Bash_NoCommand(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "20", Type: CmdBash})
	if resp.Success {
		t.Error("expected error for empty bash command")
	}
}

func TestHandleCommand_ExportHTML(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "21", Type: CmdExportHTML})
	if !resp.Success {
		t.Fatalf("expected export to succeed, got error: %s", resp.Error)
	}
	data, _ := json.Marshal(resp.Data)
	var exportData ExportHTMLData
	if err := json.Unmarshal(data, &exportData); err != nil {
		t.Fatalf("failed to unmarshal export data: %v", err)
	}
	if exportData.Path == "" {
		t.Error("expected a file path in export response")
	}
	// Clean up the temp file
	_ = os.Remove(exportData.Path)
}

func TestHandleCommand_SwitchSession_NoPath(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "22", Type: CmdSwitchSession})
	if resp.Success {
		t.Error("expected error for missing session path")
	}
}

func TestHandleCommand_GetCommands(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "23", Type: CmdGetCommands})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_GetCommands_WithPromptsAndSkills(t *testing.T) {
	sm := core.InMemorySessionManager("/tmp/rpc-test")
	a := agent.NewAgent(agent.AgentOptions{})
	session := core.NewAgentSession(core.AgentSessionOptions{
		Agent:          a,
		SessionManager: sm,
		ResourceLoader: &mockResourceLoader{
			prompts: []core.PromptTemplate{
				{Name: "fix", Description: "Fix a bug (user)", Source: "user", FilePath: "/home/.tau/agent/prompts/fix.md"},
				{Name: "review", Description: "Code review (project)", Source: "project", FilePath: "/project/.tau/prompts/review.md"},
			},
			skills: []core.Skill{
				{Name: "debug", Description: "Debug a problem", Source: "user", FilePath: "/home/.tau/agent/skills/debug/SKILL.md"},
			},
		},
		Cwd: "/tmp/rpc-test",
	})
	s := &Server{session: session}

	resp := s.handleCommand(RpcCommand{ID: "42", Type: CmdGetCommands})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	data, _ := json.Marshal(resp.Data)
	var result GetCommandsData
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Commands) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(result.Commands))
	}

	// Prompt templates
	if result.Commands[0].Name != "fix" || result.Commands[0].Source != "prompt" {
		t.Errorf("expected prompt 'fix', got %+v", result.Commands[0])
	}
	if result.Commands[1].Name != "review" || result.Commands[1].Source != "prompt" {
		t.Errorf("expected prompt 'review', got %+v", result.Commands[1])
	}

	// Skills
	if result.Commands[2].Name != "skill:debug" || result.Commands[2].Source != "skill" {
		t.Errorf("expected skill 'skill:debug', got %+v", result.Commands[2])
	}
}

func TestHandleCommand_GetForkMessages(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "24", Type: CmdGetForkMessages})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestHandleCommand_Unknown(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "25", Type: "unknown_command"})
	if resp.Success {
		t.Error("expected error for unknown command")
	}
}

func TestHandleCommand_Abort(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "26a", Type: CmdAbort})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	// Abort must not kill the session; a subsequent GetState should still work.
	resp2 := s.handleCommand(RpcCommand{ID: "26b", Type: CmdGetState})
	if !resp2.Success {
		t.Errorf("session unusable after Abort: %s", resp2.Error)
	}
}

func TestHandleCommand_AbortBash(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "26", Type: CmdAbortBash})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// handleCommand tests — response structure
// ---------------------------------------------------------------------------

func TestHandleCommand_ResponseHasCorrectID(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "test-id-42", Type: CmdGetState})
	if resp.ID != "test-id-42" {
		t.Errorf("expected ID 'test-id-42', got %q", resp.ID)
	}
}

func TestHandleCommand_ResponseHasCorrectCommand(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "1", Type: CmdGetState})
	if resp.Command != CmdGetState {
		t.Errorf("expected command %q, got %q", CmdGetState, resp.Command)
	}
	if resp.Type != "response" {
		t.Errorf("expected type 'response', got %q", resp.Type)
	}
}
