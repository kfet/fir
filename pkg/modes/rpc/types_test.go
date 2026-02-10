package rpc

import (
	"encoding/json"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
)

func TestParseRpcCommand_Prompt(t *testing.T) {
	data := `{"id":"1","type":"prompt","message":"hello"}`
	cmd, err := ParseRpcCommand([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Type != CmdPrompt {
		t.Errorf("expected type 'prompt', got %q", cmd.Type)
	}
	if cmd.ID != "1" {
		t.Errorf("expected id '1', got %q", cmd.ID)
	}
	if cmd.Message != "hello" {
		t.Errorf("expected message 'hello', got %q", cmd.Message)
	}
}

func TestParseRpcCommand_SetModel(t *testing.T) {
	data := `{"type":"set_model","provider":"anthropic","modelId":"claude-3"}`
	cmd, err := ParseRpcCommand([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Type != CmdSetModel {
		t.Errorf("expected type 'set_model', got %q", cmd.Type)
	}
	if cmd.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", cmd.Provider)
	}
	if cmd.ModelID != "claude-3" {
		t.Errorf("expected modelId 'claude-3', got %q", cmd.ModelID)
	}
}

func TestParseRpcCommand_SetAutoCompaction(t *testing.T) {
	data := `{"type":"set_auto_compaction","enabled":true}`
	cmd, err := ParseRpcCommand([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Type != CmdSetAutoCompaction {
		t.Errorf("expected type 'set_auto_compaction', got %q", cmd.Type)
	}
	if cmd.Enabled == nil || !*cmd.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestParseRpcCommand_Bash(t *testing.T) {
	data := `{"id":"b1","type":"bash","command":"echo hello"}`
	cmd, err := ParseRpcCommand([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Type != CmdBash {
		t.Errorf("expected type 'bash', got %q", cmd.Type)
	}
	if cmd.Command != "echo hello" {
		t.Errorf("expected command 'echo hello', got %q", cmd.Command)
	}
}

func TestParseRpcCommand_InvalidJSON(t *testing.T) {
	_, err := ParseRpcCommand([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseRpcCommand_AllTypes(t *testing.T) {
	types := []RpcCommandType{
		CmdPrompt, CmdSteer, CmdFollowUp, CmdAbort, CmdNewSession,
		CmdGetState, CmdSetModel, CmdCycleModel, CmdGetAvailableModels,
		CmdSetThinkingLevel, CmdCycleThinkingLevel,
		CmdSetSteeringMode, CmdSetFollowUpMode,
		CmdCompact, CmdSetAutoCompaction, CmdSetAutoRetry, CmdAbortRetry,
		CmdBash, CmdAbortBash,
		CmdGetSessionStats, CmdExportHTML, CmdSwitchSession,
		CmdFork, CmdGetForkMessages, CmdGetLastAssistantText, CmdSetSessionName,
		CmdGetMessages, CmdGetCommands,
	}
	for _, ct := range types {
		data, err := json.Marshal(RpcCommand{Type: ct})
		if err != nil {
			t.Fatalf("failed to marshal command type %s: %v", ct, err)
		}
		cmd, err := ParseRpcCommand(data)
		if err != nil {
			t.Fatalf("failed to parse command type %s: %v", ct, err)
		}
		if cmd.Type != ct {
			t.Errorf("expected type %s, got %s", ct, cmd.Type)
		}
	}
}

func TestNewSuccessResponse(t *testing.T) {
	resp := NewSuccessResponse("r1", CmdGetState, map[string]string{"key": "val"})
	if resp.Type != "response" {
		t.Errorf("expected type 'response', got %q", resp.Type)
	}
	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.Command != CmdGetState {
		t.Errorf("expected command 'get_state', got %q", resp.Command)
	}
	if resp.ID != "r1" {
		t.Errorf("expected id 'r1', got %q", resp.ID)
	}
	if resp.Error != "" {
		t.Errorf("expected no error, got %q", resp.Error)
	}
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse("r2", CmdBash, "command failed")
	if resp.Type != "response" {
		t.Errorf("expected type 'response', got %q", resp.Type)
	}
	if resp.Success {
		t.Error("expected success=false")
	}
	if resp.Error != "command failed" {
		t.Errorf("expected error 'command failed', got %q", resp.Error)
	}
}

func TestRpcResponse_MarshalJSON(t *testing.T) {
	resp := NewSuccessResponse("x", CmdPrompt, nil)
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed["type"] != "response" {
		t.Errorf("expected type 'response', got %v", parsed["type"])
	}
	if parsed["success"] != true {
		t.Errorf("expected success true, got %v", parsed["success"])
	}
	if parsed["command"] != "prompt" {
		t.Errorf("expected command 'prompt', got %v", parsed["command"])
	}
}

func TestRpcResponse_MarshalWithData(t *testing.T) {
	data := &RpcSessionState{
		ThinkingLevel:         ai.ThinkingMedium,
		IsStreaming:           true,
		SteeringMode:          "all",
		FollowUpMode:          "one-at-a-time",
		SessionID:             "sess-1",
		AutoCompactionEnabled: true,
		MessageCount:          5,
	}
	resp := NewSuccessResponse("s1", CmdGetState, data)
	jsonBytes, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// Verify it contains expected fields
	jsonStr := string(jsonBytes)
	if !contains(jsonStr, `"isStreaming":true`) {
		t.Errorf("expected isStreaming in JSON, got %s", jsonStr)
	}
	if !contains(jsonStr, `"sessionId":"sess-1"`) {
		t.Errorf("expected sessionId in JSON, got %s", jsonStr)
	}
}

func TestRpcSlashCommand_JSON(t *testing.T) {
	cmd := RpcSlashCommand{
		Name:        "help",
		Description: "Show help",
		Source:      "extension",
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed RpcSlashCommand
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Name != "help" {
		t.Errorf("expected name 'help', got %q", parsed.Name)
	}
	if parsed.Source != "extension" {
		t.Errorf("expected source 'extension', got %q", parsed.Source)
	}
}

func TestSessionStats_JSON(t *testing.T) {
	stats := SessionStats{
		SessionID:         "s1",
		UserMessages:      3,
		AssistantMessages: 3,
		ToolCalls:         2,
		ToolResults:       2,
		TotalMessages:     10,
		Tokens: TokenStats{
			Input:  1000,
			Output: 500,
			Total:  1500,
		},
		Cost: 0.05,
	}
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed SessionStats
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Tokens.Input != 1000 {
		t.Errorf("expected input tokens 1000, got %d", parsed.Tokens.Input)
	}
	if parsed.Cost != 0.05 {
		t.Errorf("expected cost 0.05, got %f", parsed.Cost)
	}
}

func TestParseExtensionUIResponse(t *testing.T) {
	data := `{"type":"extension_ui_response","id":"ext1","value":"hello"}`
	resp, err := ParseExtensionUIResponse([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Type != "extension_ui_response" {
		t.Errorf("expected type 'extension_ui_response', got %q", resp.Type)
	}
	if resp.ID != "ext1" {
		t.Errorf("expected id 'ext1', got %q", resp.ID)
	}
	if resp.Value != "hello" {
		t.Errorf("expected value 'hello', got %q", resp.Value)
	}
}

func TestParseExtensionUIResponse_Cancelled(t *testing.T) {
	data := `{"type":"extension_ui_response","id":"ext2","cancelled":true}`
	resp, err := ParseExtensionUIResponse([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Cancelled {
		t.Error("expected cancelled=true")
	}
}

func TestParseExtensionUIResponse_Confirmed(t *testing.T) {
	data := `{"type":"extension_ui_response","id":"ext3","confirmed":true}`
	resp, err := ParseExtensionUIResponse([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Confirmed == nil || !*resp.Confirmed {
		t.Error("expected confirmed=true")
	}
}

func TestRpcExtensionUIRequest_JSON(t *testing.T) {
	req := RpcExtensionUIRequest{
		Type:    "extension_ui_request",
		ID:      "req1",
		Method:  "select",
		Title:   "Choose",
		Options: []string{"a", "b", "c"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed RpcExtensionUIRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Method != "select" {
		t.Errorf("expected method 'select', got %q", parsed.Method)
	}
	if len(parsed.Options) != 3 {
		t.Errorf("expected 3 options, got %d", len(parsed.Options))
	}
}

func TestNewSessionData_JSON(t *testing.T) {
	d := NewSessionData{Cancelled: true}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed NewSessionData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !parsed.Cancelled {
		t.Error("expected cancelled=true")
	}
}

func TestGetAvailableModelsData_JSON(t *testing.T) {
	d := GetAvailableModelsData{
		Models: []ai.Model{
			{ID: "model-1", Name: "Model 1"},
			{ID: "model-2", Name: "Model 2"},
		},
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed GetAvailableModelsData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(parsed.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(parsed.Models))
	}
}

func TestBashResultData_JSON(t *testing.T) {
	d := BashResultData{
		ExitCode: 0,
		Stdout:   "hello\n",
		Stderr:   "",
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed BashResultData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", parsed.ExitCode)
	}
	if parsed.Stdout != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got %q", parsed.Stdout)
	}
}

func TestForkMessageEntry_JSON(t *testing.T) {
	d := GetForkMessagesData{
		Messages: []ForkMessageEntry{
			{EntryID: "e1", Text: "Hello"},
			{EntryID: "e2", Text: "World"},
		},
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed GetForkMessagesData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(parsed.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(parsed.Messages))
	}
	if parsed.Messages[0].EntryID != "e1" {
		t.Errorf("expected entry id 'e1', got %q", parsed.Messages[0].EntryID)
	}
}

func TestGetLastAssistantTextData_NilText(t *testing.T) {
	d := GetLastAssistantTextData{Text: nil}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed GetLastAssistantTextData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Text != nil {
		t.Errorf("expected nil text, got %v", parsed.Text)
	}
}

func TestGetLastAssistantTextData_WithText(t *testing.T) {
	text := "hello"
	d := GetLastAssistantTextData{Text: &text}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed GetLastAssistantTextData
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.Text == nil || *parsed.Text != "hello" {
		t.Errorf("expected text 'hello', got %v", parsed.Text)
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && jsonContains(s, substr)
}

func jsonContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
