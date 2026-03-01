//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Section 8: ACP mode tests

func TestACP_Initialize(t *testing.T) {
	agentDir := t.TempDir()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}` + "\n"
	out, code := runFirWithDelay(t, input, 2*time.Second, 10*time.Second,
		map[string]string{"FIR_AGENT_DIR": agentDir},
		"--mode", "acp", "--no-session")
	assertNoPanic(t, out)
	if code != 0 {
		t.Logf("exit code %d", code)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		id, _ := m["id"].(float64)
		return id == 1
	})
	if resp == nil {
		t.Fatalf("no response for id 1: %s", out)
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Fatalf("got error response: %v", resp)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", resp)
	}
	agentInfo, ok := result["agentInfo"].(map[string]any)
	if !ok {
		t.Fatalf("no agentInfo in result: %v", result)
	}
	if agentInfo["name"] != "fir" {
		t.Fatalf("expected agentInfo.name='fir', got: %v", agentInfo["name"])
	}
}

func TestACP_SessionNew(t *testing.T) {
	agentDir := t.TempDir()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}` + "\n"
	out, _ := runFirWithDelay(t, input, 2*time.Second, 10*time.Second,
		map[string]string{"FIR_AGENT_DIR": agentDir},
		"--mode", "acp", "--no-session")
	assertNoPanic(t, out)
	lines := parseJSONLines(out)

	resp := findJSONLine(lines, func(m map[string]any) bool {
		id, _ := m["id"].(float64)
		return id == 2
	})
	if resp == nil {
		t.Fatalf("no response for id 2: %s", out)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in session/new response: %v", resp)
	}
	sessionID, _ := result["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("empty sessionId: %v", result)
	}

	// Section 8c: Check session/update notification
	notification := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "method") == "session/update"
	})
	if notification != nil {
		raw, _ := json.Marshal(notification)
		s := string(raw)
		if !strings.Contains(s, "share") {
			t.Logf("session/update missing 'share' command: %s", s)
		}
		if !strings.Contains(s, "export") {
			t.Logf("session/update missing 'export' command: %s", s)
		}
	}
}

func TestACP_UnknownMethod(t *testing.T) {
	agentDir := t.TempDir()
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"agent/bogus","params":{}}` + "\n"
	out, _ := runFirWithDelay(t, input, 2*time.Second, 10*time.Second,
		map[string]string{"FIR_AGENT_DIR": agentDir},
		"--mode", "acp", "--no-session")
	assertNoPanic(t, out)
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		id, _ := m["id"].(float64)
		return id == 2
	})
	if resp == nil {
		t.Fatalf("no response for id 2: %s", out)
	}
	errObj, hasErr := resp["error"].(map[string]any)
	if !hasErr {
		t.Fatalf("expected error response for unknown method: %v", resp)
	}
	code, _ := errObj["code"].(float64)
	if code != -32601 {
		t.Fatalf("expected error code -32601, got: %v", code)
	}
}

func TestACP_MalformedJSON(t *testing.T) {
	agentDir := t.TempDir()
	out, _ := runFirWithDelay(t, "this is not json\n", 1*time.Second, 10*time.Second,
		map[string]string{"FIR_AGENT_DIR": agentDir},
		"--mode", "acp", "--no-session")
	assertNoPanic(t, out)
}
