package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestIntegrationPythonExtension spawns a real Python extension process using
// the fir_ext SDK and exercises the JSON-RPC 2.0 protocol end-to-end:
//
//  1. Writes a Python script that registers a "word_count" tool and
//     subscribes to "session_start".
//  2. Discovers the script in .fir/extensions/.
//  3. Spawns the process, performs init handshake, verifies capabilities.
//  4. Calls the tool and verifies the result.
//  5. Sends an event and verifies the process stays healthy.
//  6. Stops the process cleanly.
func TestIntegrationPythonExtension(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Locate the SDK python dir relative to this test file.
	_, thisFile, _, _ := runtime.Caller(0)
	sdkDir := filepath.Join(filepath.Dir(thisFile), "..", "sdk", "python")
	sdkDir, _ = filepath.Abs(sdkDir)
	if _, err := os.Stat(filepath.Join(sdkDir, "fir_ext.py")); err != nil {
		t.Fatalf("fir_ext.py not found at %s", sdkDir)
	}

	// Create temp project with .fir/extensions/word_count.py
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}

	script := `#!/usr/bin/env python3
import fir_ext

@fir_ext.tool(
    name="word_count",
    description="Count words in text",
    parameters={"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]},
)
def word_count(params, ctx):
    text = params.get("text", "")
    return {"count": len(text.split())}

@fir_ext.on("session_start")
def on_start(params, ctx):
    pass

fir_ext.run(name="word_count")
`
	scriptPath := filepath.Join(extDir, "word_count.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// --- Discovery (manual scan) ---
	entries, err := os.ReadDir(extDir)
	if err != nil {
		t.Fatal(err)
	}
	var foundScript string
	for _, e := range entries {
		if e.Name() == "word_count.py" {
			foundScript = filepath.Join(extDir, e.Name())
		}
	}
	if foundScript == "" {
		t.Fatal("word_count.py not found in extension dir")
	}
	info, err := os.Stat(foundScript)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatal("script not executable")
	}
	t.Log("discovered:", foundScript)

	// --- Spawn process ---
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", foundScript)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+sdkDir)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)

	// jsonrpc helpers
	type jrpcReq struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	type jrpcNotif struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	type jrpcResp struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      any              `json:"id"`
		Result  json.RawMessage  `json:"result,omitempty"`
		Error   *json.RawMessage `json:"error,omitempty"`
	}

	send := func(msg any) {
		t.Helper()
		if err := encoder.Encode(msg); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	recv := func() jrpcResp {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("recv: no data (err=%v)", scanner.Err())
		}
		var resp jrpcResp
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("recv unmarshal: %v\nraw: %s", err, scanner.Text())
		}
		return resp
	}

	// --- Init handshake ---
	send(jrpcReq{
		JSONRPC: "2.0", ID: 1, Method: "init",
		Params: map[string]any{"version": "1", "cwd": projectDir},
	})
	initResp := recv()
	if initResp.Error != nil {
		t.Fatalf("init error: %s", *initResp.Error)
	}

	var initResult struct {
		Name  string `json:"name"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		t.Fatalf("parse init result: %v", err)
	}

	if initResult.Name != "word_count" {
		t.Errorf("name = %q, want word_count", initResult.Name)
	}
	if len(initResult.Tools) != 1 || initResult.Tools[0].Name != "word_count" {
		t.Errorf("tools = %+v, want [word_count]", initResult.Tools)
	}
	hasSessionStart := false
	for _, e := range initResult.Events {
		if e == "session_start" {
			hasSessionStart = true
		}
	}
	if !hasSessionStart {
		t.Errorf("events = %v, want session_start", initResult.Events)
	}
	t.Log("handshake OK:", initResult.Name, "tools:", len(initResult.Tools), "events:", initResult.Events)

	// --- Call tool ---
	send(jrpcReq{
		JSONRPC: "2.0", ID: 2, Method: "tool_call",
		Params: map[string]any{
			"tool_call_id": "tc-1",
			"name":         "word_count",
			"params":       map[string]any{"text": "hello world foo bar"},
		},
	})
	toolResp := recv()
	if toolResp.Error != nil {
		t.Fatalf("tool_call error: %s", *toolResp.Error)
	}

	var toolResult map[string]any
	if err := json.Unmarshal(toolResp.Result, &toolResult); err != nil {
		t.Fatalf("parse tool result: %v", err)
	}
	count, ok := toolResult["count"].(float64)
	if !ok || int(count) != 4 {
		t.Errorf("count = %v, want 4", toolResult["count"])
	}
	t.Log("tool_call OK: count =", int(count))

	// --- Call unknown tool ---
	send(jrpcReq{
		JSONRPC: "2.0", ID: 3, Method: "tool_call",
		Params: map[string]any{"name": "nonexistent", "params": map[string]any{}},
	})
	errResp := recv()
	if errResp.Error == nil {
		t.Error("expected error for unknown tool, got success")
	} else {
		t.Log("unknown tool correctly returned error")
	}

	// --- Emit event (fire-and-forget notification) ---
	send(jrpcNotif{
		JSONRPC: "2.0", Method: "event/session_start",
		Params: map[string]any{},
	})
	// Verify process still alive with another tool call.

	send(jrpcReq{
		JSONRPC: "2.0", ID: 4, Method: "tool_call",
		Params: map[string]any{
			"tool_call_id": "tc-2",
			"name":         "word_count",
			"params":       map[string]any{"text": "still alive"},
		},
	})
	aliveResp := recv()
	if aliveResp.Error != nil {
		t.Fatalf("post-event tool_call error: %s", *aliveResp.Error)
	}
	var aliveResult map[string]any
	if err := json.Unmarshal(aliveResp.Result, &aliveResult); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c, _ := aliveResult["count"].(float64); int(c) != 2 {
		t.Errorf("post-event count = %v, want 2", aliveResult["count"])
	}
	t.Log("event + post-event tool_call OK")

	// --- Clean shutdown ---
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Logf("process exited with: %v (expected)", err)
		} else {
			t.Log("process exited cleanly")
		}
	case <-time.After(5 * time.Second):
		t.Error("process did not exit within 5s after stdin close")
		_ = cmd.Process.Kill()
	}
}
