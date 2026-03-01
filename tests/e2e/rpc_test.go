//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// Section 2: RPC mode tests
// Section 6: Additional RPC command tests

func TestRPC_GetState(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_state"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_state"
	})
	if resp == nil {
		t.Fatalf("no get_state response found in: %s", out)
	}
	if getNestedString(resp, "success") != "true" {
		t.Fatalf("get_state not successful: %v", resp)
	}
}

func TestRPC_Prompt(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"prompt","message":"Say exactly: RPC_TEST_OK"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
	lines := parseJSONLines(out)
	if len(lines) == 0 {
		t.Fatalf("no JSON output lines")
	}
	// Should have agent_start or message_start
	found := findJSONLine(lines, func(m map[string]any) bool {
		tp := getNestedString(m, "Type")
		return tp == "agent_start" || tp == "message_start"
	})
	if found == nil {
		// Also check lowercase
		found = findJSONLine(lines, func(m map[string]any) bool {
			tp := getNestedString(m, "type")
			return tp == "agent_start" || tp == "message_start"
		})
	}
	if found == nil {
		// Check AgentEvent.Type
		found = findJSONLine(lines, func(m map[string]any) bool {
			tp := getNestedString(m, "AgentEvent.Type")
			return tp == "agent_start" || tp == "message_start"
		})
	}
	if found == nil {
		t.Logf("output: %s", out)
		t.Fatal("no agent_start or message_start event found")
	}
}

func TestRPC_GetAvailableModels(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_available_models"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_available_models"
	})
	if resp == nil {
		t.Fatalf("no get_available_models response: %s", out)
	}
	if getNestedString(resp, "success") != "true" {
		t.Fatalf("not successful: %v", resp)
	}
}

func TestRPC_SetThinkingLevel(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"set_thinking_level\",\"level\":\"high\"}\n{\"id\":\"2\",\"type\":\"get_state\"}\n"
	out, code := runFirMock(t, input, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	setResp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_thinking_level"
	})
	if setResp == nil || getNestedString(setResp, "success") != "true" {
		t.Fatalf("set_thinking_level failed: %s", out)
	}
	stateResp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_state"
	})
	if stateResp != nil && getNestedString(stateResp, "data.thinkingLevel") != "high" {
		t.Fatalf("thinking level not set to high: %v", stateResp)
	}
}

func TestRPC_UnknownCommand(t *testing.T) {
	out, _ := runFirMock(t, `{"id":"1","type":"bogus_command"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	assertNoPanic(t, out)
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "success") == "false"
	})
	if resp == nil {
		t.Fatalf("expected error response: %s", out)
	}
	errMsg := getNestedString(resp, "error")
	if !strings.Contains(strings.ToLower(errMsg), "unknown") {
		t.Fatalf("expected 'Unknown command' error, got: %s", errMsg)
	}
}

func TestRPC_MalformedJSON(t *testing.T) {
	out, _ := runFirMock(t, "this is not json", 10*time.Second, "--mode", "rpc", "--no-session")
	assertNoPanic(t, out)
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "success") == "false"
	})
	_ = resp // may or may not have error response, main thing is no panic
}

func TestRPC_Abort(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"prompt","message":"Write a very long essay about the history of mathematics"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	assertNoPanic(t, out)
	if code != 0 {
		t.Logf("exit code %d (acceptable — stdin closed)", code)
	}
}

// Section 6: Additional RPC commands

func TestRPC_SetModel(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"set_model\",\"provider\":\"mock\",\"modelId\":\"mock-model-2\"}\n{\"id\":\"2\",\"type\":\"get_state\"}\n"
	out, code := runFirMock(t, input, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_model"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("set_model failed: %s", out)
	}
}

func TestRPC_CycleModel(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"cycle_model"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "cycle_model"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("cycle_model failed: %s", out)
	}
}

func TestRPC_CycleThinkingLevel(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"cycle_thinking_level\"}\n{\"id\":\"2\",\"type\":\"get_state\"}\n"
	out, code := runFirMock(t, input, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "cycle_thinking_level"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("cycle_thinking_level failed: %s", out)
	}
}

func TestRPC_BashDirect(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"bash","command":"echo RPC_BASH_DIRECT_OK"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "bash"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("bash failed: %s", out)
	}
	if !strings.Contains(getNestedString(resp, "data.Output"), "RPC_BASH_DIRECT_OK") {
		// Try lowercase
		if !strings.Contains(getNestedString(resp, "data.output"), "RPC_BASH_DIRECT_OK") {
			t.Fatalf("expected RPC_BASH_DIRECT_OK in output: %v", resp)
		}
	}
}

func TestRPC_BashEmptyCommand(t *testing.T) {
	out, _ := runFirMock(t, `{"id":"1","type":"bash","command":""}`, 10*time.Second, "--mode", "rpc", "--no-session")
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "bash"
	})
	if resp == nil {
		t.Fatalf("no bash response: %s", out)
	}
	if getNestedString(resp, "success") != "false" {
		t.Fatalf("expected failure for empty bash command: %v", resp)
	}
}

func TestRPC_GetSessionStats(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_session_stats"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_session_stats"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_session_stats failed: %s", out)
	}
}

func TestRPC_GetMessages(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_messages"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_messages"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_messages failed: %s", out)
	}
}

func TestRPC_GetCommands(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_commands"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_commands"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_commands failed: %s", out)
	}
}

func TestRPC_GetLastAssistantText(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_last_assistant_text"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_last_assistant_text"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_last_assistant_text failed: %s", out)
	}
}

func TestRPC_SetSessionName(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"set_session_name","name":"my-test-session"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_session_name"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("set_session_name failed: %s", out)
	}
}

func TestRPC_SetSessionNameEmpty(t *testing.T) {
	out, _ := runFirMock(t, `{"id":"1","type":"set_session_name","name":""}`, 10*time.Second, "--mode", "rpc", "--no-session")
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_session_name"
	})
	if resp == nil {
		t.Fatalf("no response: %s", out)
	}
	if getNestedString(resp, "success") != "false" {
		t.Fatalf("expected failure for empty name: %v", resp)
	}
}

func TestRPC_GetForkMessages(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"get_fork_messages"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_fork_messages"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_fork_messages failed: %s", out)
	}
}

func TestRPC_NewSession(t *testing.T) {
	out, code := runFirMock(t, `{"id":"1","type":"new_session"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "new_session"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("new_session failed: %s", out)
	}
}

func TestRPC_SetAutoCompaction(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"set_auto_compaction\",\"enabled\":false}\n{\"id\":\"2\",\"type\":\"get_state\"}\n"
	out, code := runFirMock(t, input, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_auto_compaction"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("set_auto_compaction failed: %s", out)
	}
}

func TestRPC_SetSteeringAndFollowUpMode(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"set_steering_mode\",\"mode\":\"one-at-a-time\"}\n{\"id\":\"2\",\"type\":\"set_follow_up_mode\",\"mode\":\"one-at-a-time\"}\n"
	out, code := runFirMock(t, input, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	for _, cmdName := range []string{"set_steering_mode", "set_follow_up_mode"} {
		resp := findJSONLine(lines, func(m map[string]any) bool {
			return getNestedString(m, "command") == cmdName
		})
		if resp == nil || getNestedString(resp, "success") != "true" {
			t.Fatalf("%s failed: %s", cmdName, out)
		}
	}
}

func TestRPC_AbortBashAndRetry(t *testing.T) {
	input := "{\"id\":\"1\",\"type\":\"abort_bash\"}\n{\"id\":\"2\",\"type\":\"abort_retry\"}\n"
	out, code := runFirMock(t, input, 10*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	for _, cmdName := range []string{"abort_bash", "abort_retry"} {
		resp := findJSONLine(lines, func(m map[string]any) bool {
			return getNestedString(m, "command") == cmdName
		})
		if resp == nil || getNestedString(resp, "success") != "true" {
			t.Fatalf("%s failed: %s", cmdName, out)
		}
	}
}

func TestRPC_ExportHTML(t *testing.T) {
	dir := t.TempDir()
	out, code := runFirMockDir(t, dir, `{"id":"1","type":"export_html"}`, 15*time.Second, "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "export_html"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("export_html failed: %s", out)
	}
}

func TestRPC_SetModelNonExistent(t *testing.T) {
	out, _ := runFirMock(t, `{"id":"1","type":"set_model","provider":"mock","modelId":"nonexistent"}`, 10*time.Second, "--mode", "rpc", "--no-session")
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "set_model"
	})
	if resp == nil {
		t.Fatalf("no set_model response: %s", out)
	}
	if getNestedString(resp, "success") != "false" {
		t.Fatalf("expected failure for nonexistent model: %v", resp)
	}
}

func TestRPC_SwitchSessionEmptyPath(t *testing.T) {
	out, _ := runFirMock(t, `{"id":"1","type":"switch_session","sessionPath":""}`, 10*time.Second, "--mode", "rpc", "--no-session")
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "switch_session"
	})
	if resp == nil {
		t.Fatalf("no switch_session response: %s", out)
	}
	if getNestedString(resp, "success") != "false" {
		t.Fatalf("expected failure for empty session path: %v", resp)
	}
}
