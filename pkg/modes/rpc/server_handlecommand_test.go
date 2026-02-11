package rpc

import (
	"encoding/json"
	"testing"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
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
	// Fork is not yet implemented, should return error
	if resp.Success {
		t.Error("expected error for fork (not yet implemented)")
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

func TestHandleCommand_Bash(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "20", Type: CmdBash})
	if resp.Success {
		t.Error("expected error for unimplemented bash")
	}
}

func TestHandleCommand_ExportHTML(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "21", Type: CmdExportHTML})
	if resp.Success {
		t.Error("expected error for unimplemented export")
	}
}

func TestHandleCommand_SwitchSession(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "22", Type: CmdSwitchSession})
	if resp.Success {
		t.Error("expected error for unimplemented switch session")
	}
}

func TestHandleCommand_GetCommands(t *testing.T) {
	s := testServer()
	resp := s.handleCommand(RpcCommand{ID: "23", Type: CmdGetCommands})
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
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
