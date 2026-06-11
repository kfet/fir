package providers

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	firlog "github.com/kfet/fir/pkg/log"
)

// withTraceLog initializes firlog to a tempfile at Trace level for the
// duration of the test and returns the captured file contents.
func withTraceLog(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.log")
	cleanup, err := firlog.Init(true, path, firlog.RotateConfig{})
	if err != nil {
		t.Fatalf("firlog.Init: %v", err)
	}
	prev := firlog.CurrentLevel()
	firlog.SetLevel(firlog.LevelTrace)
	defer func() {
		firlog.SetLevel(prev)
		cleanup()
	}()
	fn()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestTraceWireMessages_AnthropicShape(t *testing.T) {
	params := map[string]any{
		"system": []map[string]any{{"type": "text", "text": "you are…"}},
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "hello"},
			}},
			{"role": "assistant", "content": []map[string]any{
				{"type": "thinking", "thinking": "", "signature": strings.Repeat("s", 64)},
				{"type": "text", "text": "hi"},
				{"type": "tool_use", "id": "tu_1", "name": "bash", "input": map[string]any{}},
			}},
		},
	}
	out := withTraceLog(t, func() { traceWireMessages("anthropic", params) })
	for _, want := range []string{
		`"anthropic wire"`, `"messages":2`, `"systemBlocks":1`,
		`"role":"assistant"`, `thinking(th=0,sig=64)`, `tool_use(bash)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in trace output:\n%s", want, out)
		}
	}
}

func TestTraceWireMessages_OpenAIResponsesShape(t *testing.T) {
	body := map[string]any{
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{
				{"type": "input_text", "text": "hello"},
			}},
			{"type": "function_call", "name": "bash", "call_id": "fc_1"},
			{"type": "reasoning", "summary": []any{map[string]any{"text": "."}}, "encrypted_content": strings.Repeat("e", 32)},
		},
	}
	out := withTraceLog(t, func() { traceWireMessages("openai-responses", body) })
	for _, want := range []string{
		`"openai-responses wire"`, `input_text(5)`,
		`function_call(bash)`, `reasoning(parts=1,enc=32)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in trace output:\n%s", want, out)
		}
	}
}

func TestTraceWireMessages_GoogleShape(t *testing.T) {
	body := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{
				{"text": "hello"},
			}},
			{"role": "model", "parts": []map[string]any{
				{"text": "thinking…", "thought": true},
				{"functionCall": map[string]any{"name": "bash"}},
			}},
		},
	}
	out := withTraceLog(t, func() { traceWireMessages("google", body) })
	for _, want := range []string{
		`"google wire"`, `text(5)`, `thought`, `functionCall(bash)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in trace output:\n%s", want, out)
		}
	}
}

func TestTraceWireMessages_AcceptsBytesAndString(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hi"},
	}}
	raw, _ := json.Marshal(body)
	out := withTraceLog(t, func() { traceWireMessages("p", raw) })
	if !strings.Contains(out, `"p wire"`) {
		t.Errorf("bytes input not handled:\n%s", out)
	}
	out = withTraceLog(t, func() { traceWireMessages("p", string(raw)) })
	if !strings.Contains(out, `"p wire"`) {
		t.Errorf("string input not handled:\n%s", out)
	}
}

func TestTraceWireMessages_NoOpWhenDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	cleanup, err := firlog.Init(true, path, firlog.RotateConfig{})
	if err != nil {
		t.Fatalf("firlog.Init: %v", err)
	}
	defer cleanup()
	firlog.SetLevel(slog.LevelInfo)
	traceWireMessages("anthropic", map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "wire") {
		t.Errorf("expected no wire-trace output when level=Info, got: %s", b)
	}
}
