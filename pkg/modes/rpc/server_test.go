package rpc

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Server construction tests
// ---------------------------------------------------------------------------

func TestNewServer_NilSession(t *testing.T) {
	s := NewServer(nil)
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}

func TestNewServerWithIO_NilArgs(t *testing.T) {
	s := NewServerWithIO(nil, nil, nil)
	if s == nil {
		t.Fatal("expected non-nil server")
	}
}

// ---------------------------------------------------------------------------
// Server outputJSON
// ---------------------------------------------------------------------------

func TestServer_OutputJSON(t *testing.T) {
	// The outputJSON method should not panic when writing to a valid writer
	var buf mockWriter
	s := NewServerWithIO(nil, nil, &buf)
	s.outputJSON(map[string]string{"key": "val"})
	if len(buf.data) == 0 {
		t.Error("expected output")
	}
}

func TestServer_OutputJSON_InvalidData(t *testing.T) {
	// Channels can't be marshaled to JSON - should not panic
	var buf mockWriter
	s := NewServerWithIO(nil, nil, &buf)
	s.outputJSON(make(chan int)) // unmarshalable
	if len(buf.data) != 0 {
		t.Error("expected no output for unmarshalable data")
	}
}

type mockWriter struct {
	data []byte
}

func (w *mockWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Command type constants
// ---------------------------------------------------------------------------

func TestCommandTypeConstants(t *testing.T) {
	expected := map[RpcCommandType]string{
		CmdPrompt:               "prompt",
		CmdSteer:                "steer",
		CmdFollowUp:             "follow_up",
		CmdAbort:                "abort",
		CmdNewSession:           "new_session",
		CmdGetState:             "get_state",
		CmdSetModel:             "set_model",
		CmdCycleModel:           "cycle_model",
		CmdGetAvailableModels:   "get_available_models",
		CmdSetThinkingLevel:     "set_thinking_level",
		CmdCycleThinkingLevel:   "cycle_thinking_level",
		CmdSetSteeringMode:      "set_steering_mode",
		CmdSetFollowUpMode:      "set_follow_up_mode",
		CmdCompact:              "compact",
		CmdSetAutoCompaction:    "set_auto_compaction",
		CmdSetAutoRetry:         "set_auto_retry",
		CmdAbortRetry:           "abort_retry",
		CmdBash:                 "bash",
		CmdAbortBash:            "abort_bash",
		CmdGetSessionStats:      "get_session_stats",
		CmdExportHTML:           "export_html",
		CmdSwitchSession:        "switch_session",
		CmdFork:                 "fork",
		CmdGetForkMessages:      "get_fork_messages",
		CmdGetLastAssistantText: "get_last_assistant_text",
		CmdSetSessionName:       "set_session_name",
		CmdGetMessages:          "get_messages",
		CmdGetCommands:          "get_commands",
	}

	for cmd, val := range expected {
		if string(cmd) != val {
			t.Errorf("expected %q = %q, got %q", cmd, val, string(cmd))
		}
	}
}
