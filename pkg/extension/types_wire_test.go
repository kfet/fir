package extension

import (
	"encoding/json"
	"testing"
)

// TestWireCompat_OkResult guards the most common ack shape.
func TestWireCompat_OkResult(t *testing.T) {
	b, _ := json.Marshal(okTrue)
	if string(b) != `{"ok":true}` {
		t.Fatalf("okTrue: %s", b)
	}
	b, _ = json.Marshal(OkResult{Ok: false})
	if string(b) != `{"ok":false}` {
		t.Fatalf("OkResult{false}: %s", b)
	}
}

// TestWireCompat_GetSessionResults — these are the four inspection helpers
// extensions rely on. Field names must remain stable.
func TestWireCompat_GetSessionResults(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{GetSessionDataResult{Value: "v", Ok: true}, `{"value":"v","ok":true}`},
		{GetSessionDataResult{}, `{"value":"","ok":false}`},
		{GetSessionFileResult{Path: "/x"}, `{"path":"/x"}`},
		{GetSessionIDResult{ID: "abc"}, `{"id":"abc"}`},
		{GetSessionNameResult{Name: "n"}, `{"name":"n"}`},
		{SideQueryResult{Ok: true, Text: "t"}, `{"ok":true,"text":"t"}`},
	}
	for _, tc := range cases {
		b, _ := json.Marshal(tc.v)
		if string(b) != tc.want {
			t.Errorf("got %s want %s", b, tc.want)
		}
	}
}

// TestWireCompat_EventPayloads pins the JSON shape of every event payload
// emitted by the bridge. A change here means a wire-protocol break.
func TestWireCompat_EventPayloads(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"session_end", SessionEndPayload{Reason: "normal"}, `{"reason":"normal"}`},
		{"session_end_err", SessionEndPayload{Reason: "x", Error: "e"}, `{"reason":"x","error":"e"}`},
		{"session_named", SessionNamedPayload{Name: "n"}, `{"name":"n"}`},
		{"session_update_named", SessionUpdatePayload{Type: "session_named", SessionName: "s"},
			`{"type":"session_named","session_name":"s"}`},
		{"session_update_plan", SessionUpdatePayload{Type: "plan_update", SessionName: "s",
			Plan: &PlanInfo{Total: 3, Completed: 1, Metadata: map[string]string{"k": "v"}}},
			`{"type":"plan_update","session_name":"s","plan":{"total":3,"completed":1,"metadata":{"k":"v"}}}`},
		{"tool_exec_start", ToolExecutionStartPayload{ToolCallID: "tc1", ToolName: "bash"},
			`{"tool_call_id":"tc1","tool_name":"bash"}`},
		{"tool_exec_end_ok", ToolExecutionEndPayload{ToolCallID: "tc1", ToolName: "bash", IsError: false},
			`{"tool_call_id":"tc1","tool_name":"bash","is_error":false}`},
		{"tool_exec_end_err", ToolExecutionEndPayload{ToolCallID: "tc1", ToolName: "bash", IsError: true, ErrorText: "boom"},
			`{"tool_call_id":"tc1","tool_name":"bash","is_error":true,"error_text":"boom"}`},
		{"session_start_id_only", SessionStartPayload{SessionID: "s"}, `{"session_id":"s"}`},
		{"session_start_with_data", SessionStartPayload{SessionID: "s", SessionData: map[string]string{"k": "v"}},
			`{"session_id":"s","session_data":{"k":"v"}}`},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if string(b) != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, b, tc.want)
		}
	}
}

// TestWireCompat_HookPayloads pins the outbound hook-call envelopes.
func TestWireCompat_HookPayloads(t *testing.T) {
	// tool_call (direct invocation): no tool_name, just name.
	b, _ := json.Marshal(ToolCallHookPayload{ToolCallID: "tc1", Name: "my_tool", Params: map[string]any{"x": 1}})
	if string(b) != `{"tool_call_id":"tc1","name":"my_tool","params":{"x":1}}` {
		t.Errorf("tool_call: %s", b)
	}
	// hook/tool_call (interceptor): no name, just tool_name.
	b, _ = json.Marshal(ToolCallHookPayload{ToolCallID: "tc2", ToolName: "bash", Params: map[string]any{}})
	if string(b) != `{"tool_call_id":"tc2","tool_name":"bash","params":{}}` {
		t.Errorf("hook/tool_call: %s", b)
	}
	// hook/command.
	b, _ = json.Marshal(CommandHookPayload{Name: "cmd", Args: []string{"a", "b"}})
	if string(b) != `{"name":"cmd","args":["a","b"]}` {
		t.Errorf("hook/command: %s", b)
	}
}
