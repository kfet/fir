package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestReexecSessionDataFlow verifies the full reexec persistence round-trip:
//
//  1. Extension subscribes to session_shutdown and session_start.
//  2. On session_shutdown it calls set_session_data("key", "value").
//  3. ShutdownAndCollect fires session_shutdown, waits, then reads the data.
//  4. EmitSessionStartWithData seeds the data and fires session_start with
//     params["session_data"] containing the saved value.
//  5. The extension's session_start handler receives the data.
func TestReexecSessionDataFlow(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	sdkDir := filepath.Join(filepath.Dir(thisFile), "..", "sdk", "python")
	sdkDir, _ = filepath.Abs(sdkDir)

	// A minimal extension that:
	//   • on session_shutdown calls set_session_data("mykey", "myvalue")
	//   • on session_start records whatever is in params["session_data"]["mykey"]
	script := `#!/usr/bin/env python3
import fir_ext

received_on_start = None

@fir_ext.on("session_shutdown")
def on_shutdown(params, ctx):
    ctx.set_session_data("mykey", "myvalue")

@fir_ext.on("session_start")
def on_start(params, ctx):
    global received_on_start
    sd = (params or {}).get("session_data") or {}
    received_on_start = sd.get("mykey", "__missing__")
    # Use set_status to echo back what we received (no args needed).
    ctx.set_status("reexec_data_received:" + received_on_start)

fir_ext.run(name="reexec_test")
`

	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	os.MkdirAll(extDir, 0o755)
	scriptPath := filepath.Join(extDir, "reexec_test.py")
	os.WriteFile(scriptPath, []byte(script), 0o755)

	type jrpcMsg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *int            `json:"id,omitempty"`
		Method  string          `json:"method,omitempty"`
		Params  json.RawMessage `json:"params,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   json.RawMessage `json:"error,omitempty"`
	}

	spawnProc := func(t *testing.T) (
		send func(any),
		recv func() jrpcMsg,
		kill func(),
	) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, "python3", scriptPath)
		cmd.Env = append(os.Environ(), "PYTHONPATH="+sdkDir)
		cmd.Stderr = os.Stderr
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		enc := json.NewEncoder(stdin)
		sc := bufio.NewScanner(stdout)
		send = func(v any) {
			t.Helper()
			if err := enc.Encode(v); err != nil {
				t.Logf("send err: %v", err)
			}
		}
		recv = func() jrpcMsg {
			t.Helper()
			done := make(chan jrpcMsg, 1)
			go func() {
				if sc.Scan() {
					var m jrpcMsg
					json.Unmarshal(sc.Bytes(), &m)
					done <- m
				}
			}()
			select {
			case m := <-done:
				return m
			case <-time.After(5 * time.Second):
				t.Fatal("timeout waiting for message from extension")
				return jrpcMsg{}
			}
		}
		kill = func() {
			stdin.Close()
			cmd.Process.Kill()
			cmd.Wait()
			cancel()
		}
		return
	}

	nextID := 0
	id := func() *int { nextID++; v := nextID; return &v }

	// -----------------------------------------------------------------------
	// Phase 1: init, then simulate session_shutdown to trigger set_session_data
	// -----------------------------------------------------------------------
	send1, recv1, kill1 := spawnProc(t)
	defer kill1()

	// Init
	send1(map[string]any{"jsonrpc": "2.0", "id": id(), "method": "init",
		"params": map[string]any{"version": "1", "cwd": projectDir}})
	initResp := recv1()
	if initResp.Error != nil {
		t.Fatalf("init error: %s", initResp.Error)
	}
	t.Logf("init ok: %s", initResp.Result)

	// session_shutdown notification — extension will call set_session_data
	send1(map[string]any{"jsonrpc": "2.0", "method": "event/session_shutdown", "params": nil})

	// The extension calls set_session_data as an inbound RPC request back to us.
	// We need to service it (respond ok) and record the key/value.
	var savedKey, savedValue string
	var sessionDataMsg jrpcMsg
	// Collect messages until we see set_session_data or timeout.
	deadline := time.After(3 * time.Second)
	gotSetData := false
	for !gotSetData {
		ch := make(chan jrpcMsg, 1)
		go func() {
			m := recv1()
			ch <- m
		}()
		select {
		case m := <-ch:
			sessionDataMsg = m
			if m.Method == "set_session_data" {
				var p struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				}
				json.Unmarshal(m.Params, &p)
				savedKey = p.Key
				savedValue = p.Value
				// Respond ok
				send1(map[string]any{"jsonrpc": "2.0", "id": m.ID,
					"result": map[string]any{"ok": true}})
				gotSetData = true
			} else {
				t.Logf("got other msg during shutdown: method=%q result=%s", m.Method, m.Result)
			}
		case <-deadline:
			t.Fatalf("timeout: never received set_session_data from extension (last msg: method=%q)", sessionDataMsg.Method)
		}
	}

	t.Logf("set_session_data: key=%q value=%q", savedKey, savedValue)
	if savedKey != "mykey" || savedValue != "myvalue" {
		t.Errorf("expected key=mykey value=myvalue, got key=%q value=%q", savedKey, savedValue)
	}

	kill1()

	// -----------------------------------------------------------------------
	// Phase 2: new process (simulating post-reexec), fire session_start with
	// the saved session_data and verify the extension receives it.
	// -----------------------------------------------------------------------
	send2, recv2, kill2 := spawnProc(t)
	defer kill2()

	send2(map[string]any{"jsonrpc": "2.0", "id": id(), "method": "init",
		"params": map[string]any{"version": "1", "cwd": projectDir}})
	recv2() // consume init response

	// session_start with session_data populated from the sidecar
	send2(map[string]any{"jsonrpc": "2.0", "method": "event/session_start",
		"params": map[string]any{
			"session_data": map[string]string{savedKey: savedValue},
		}})

	// The extension calls set_status with "reexec_data_received:<value>".
	// Collect until we see it.
	deadline2 := time.After(5 * time.Second)
	gotEcho := false
	for !gotEcho {
		ch := make(chan jrpcMsg, 1)
		go func() { ch <- recv2() }()
		select {
		case m := <-ch:
			t.Logf("phase2 msg: method=%q result=%s params=%s", m.Method, m.Result, m.Params)
			if m.Method == "set_status" {
				var p struct {
					Status string `json:"status"`
				}
				json.Unmarshal(m.Params, &p)
				if strings.HasPrefix(p.Status, "reexec_data_received:") {
					got := strings.TrimPrefix(p.Status, "reexec_data_received:")
					if got != savedValue {
						t.Errorf("session_start got session_data[%q]=%q, want %q", savedKey, got, savedValue)
					} else {
						t.Logf("✓ extension received session_data[%q]=%q after reexec", savedKey, got)
					}
					// Respond so extension doesn't hang.
					send2(map[string]any{"jsonrpc": "2.0", "id": m.ID,
						"result": map[string]any{"ok": true}})
					gotEcho = true
				}
			} else if m.ID != nil {
				// Service any other inbound request with a generic ok.
				send2(map[string]any{"jsonrpc": "2.0", "id": m.ID,
					"result": map[string]any{"ok": true}})
			}
		case <-deadline2:
			t.Fatal("timeout: extension never echoed back session_data in session_start")
		}
	}
}
