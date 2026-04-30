//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
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

// Section 8d: interactive OAuth (`_meta.auth.interactive`) — exercises the
// two-call protocol's routing without involving a real OAuth provider.
//
//   - cancel of an unknown id is a no-op that still returns state="cancelled".
//   - call 2 (redirect) without a matching call 1 must error.
//
// The ACP SDK dispatches requests concurrently, so we drive stdin
// sequentially: write initialize, wait for its response (so authMethods is
// populated), then write the authenticate calls.
func TestACP_InteractiveAuth_CancelAndOrphanRedirect(t *testing.T) {
	agentDir := t.TempDir()
	cmd := exec.Command(firBinary, "--mode", "acp", "--no-session")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "FIR_AGENT_DIR="+agentDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Stream stdout into a channel of parsed JSON lines.
	respCh := make(chan map[string]any, 32)
	go func() {
		defer close(respCh)
		dec := json.NewDecoder(stdout)
		for {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				return
			}
			respCh <- m
		}
	}()

	waitForID := func(id float64, timeout time.Duration) map[string]any {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case m, ok := <-respCh:
				if !ok {
					t.Fatalf("stdout closed before id=%v response", id)
				}
				if got, _ := m["id"].(float64); got == id {
					return m
				}
			case <-deadline:
				t.Fatalf("timeout waiting for id=%v response", id)
			}
		}
	}

	io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}`+"\n")
	initResp := waitForID(1, 15*time.Second)

	// Discover an oauth-* method or skip — depends on Python extensions.
	res, _ := initResp["result"].(map[string]any)
	rawMethods, _ := res["authMethods"].([]any)
	var oauthMethodID string
	for _, am := range rawMethods {
		mm, _ := am.(map[string]any)
		id, _ := mm["id"].(string)
		if strings.HasPrefix(id, "oauth-") {
			oauthMethodID = id
			break
		}
	}
	if oauthMethodID == "" {
		t.Skipf("no oauth-* auth method available (Python auth extensions not active)")
	}

	// Cancel of an unknown id → state="cancelled", no error.
	io.WriteString(stdin, `{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"`+oauthMethodID+`","_meta":{"auth":{"interactive":true,"cancel":true,"id":"auth-bogus"}}}}`+"\n")
	cancelResp := waitForID(2, 10*time.Second)
	if _, hasErr := cancelResp["error"]; hasErr {
		t.Fatalf("cancel should not error, got: %v", cancelResp)
	}
	cres, _ := cancelResp["result"].(map[string]any)
	meta, _ := cres["_meta"].(map[string]any)
	auth, _ := meta["auth"].(map[string]any)
	if state, _ := auth["state"].(string); state != "cancelled" {
		t.Fatalf("cancel state = %q, want cancelled (resp: %v)", state, cancelResp)
	}

	// Redirect with no pending login → error mentioning "no pending".
	io.WriteString(stdin, `{"jsonrpc":"2.0","id":3,"method":"authenticate","params":{"methodId":"`+oauthMethodID+`","_meta":{"auth":{"interactive":true,"id":"auth-bogus","redirect":"https://localhost/cb?code=1&state=2"}}}}`+"\n")
	orphanResp := waitForID(3, 10*time.Second)
	errObj, hasErr := orphanResp["error"].(map[string]any)
	if !hasErr {
		t.Fatalf("orphan redirect should error, got: %v", orphanResp)
	}
	msg, _ := errObj["message"].(string)
	data, _ := errObj["data"].(map[string]any)
	dataMsg, _ := data["error"].(string)
	if !strings.Contains(strings.ToLower(msg+" "+dataMsg), "no pending") {
		t.Fatalf("error text should mention 'no pending'; got msg=%q data=%q", msg, dataMsg)
	}

	stdin.Close()
}
