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

// TestScheduleReexecPersistence verifies that the schedule extension correctly
// saves active schedules on session_shutdown and restores them on session_start,
// which is the mechanism that makes /schedule survive a /reexec.
//
// This is the protocol-level regression test for the bug where
// handleReexecCommand called CollectSessionData() before emitting
// session_shutdown, so the extension's on_session_shutdown handler never ran
// and schedules were silently dropped.
func TestScheduleReexecPersistence(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	sdkDir := filepath.Join(filepath.Dir(thisFile), "..", "sdk", "python")
	sdkDir, _ = filepath.Abs(sdkDir)

	extDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "resources", "builtin_extensions")
	extDir, _ = filepath.Abs(extDir)
	scriptPath := filepath.Join(extDir, "schedule.py")

	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("schedule.py not found at %s: %v", scriptPath, err)
	}

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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
			case <-time.After(8 * time.Second):
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

	// serviceRequest sends a generic ok response for any inbound request.
	serviceOK := func(send func(any), m jrpcMsg) {
		if m.ID != nil {
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"ok": true}})
		}
	}

	// -----------------------------------------------------------------------
	// Phase 1: init → /schedule 30m → session_shutdown → collect set_session_data
	// -----------------------------------------------------------------------
	send1, recv1, kill1 := spawnProc(t)
	defer kill1()

	// Handshake
	send1(map[string]any{"jsonrpc": "2.0", "id": id(), "method": "init",
		"params": map[string]any{"version": "1", "cwd": t.TempDir()}})
	initResp := recv1()
	if initResp.Error != nil {
		t.Fatalf("init error: %s", initResp.Error)
	}
	t.Logf("init ok: %s", initResp.Result)

	// Dispatch /schedule 30m via hook/command.
	cmdID := id()
	send1(map[string]any{
		"jsonrpc": "2.0",
		"id":      cmdID,
		"method":  "hook/command",
		"params":  map[string]any{"name": "schedule", "args": []string{"30m"}},
	})

	// Collect messages until we see the command response.
	// The countdown thread may emit set_status calls concurrently — service them.
	deadline := time.After(5 * time.Second)
	gotCmdResp := false
	for !gotCmdResp {
		ch := make(chan jrpcMsg, 1)
		go func() { ch <- recv1() }()
		select {
		case m := <-ch:
			switch {
			case m.ID != nil && *m.ID == *cmdID && m.Result != nil:
				// This is the command response.
				var r map[string]any
				json.Unmarshal(m.Result, &r)
				t.Logf("schedule command result: %v", r)
				if msg, _ := r["message"].(string); !strings.Contains(msg, "Scheduled") {
					t.Errorf("expected 'Scheduled' in command response, got: %q", msg)
				}
				gotCmdResp = true
			case m.Method == "set_status":
				serviceOK(send1, m)
			default:
				serviceOK(send1, m)
			}
		case <-deadline:
			t.Fatal("timeout waiting for schedule command response")
		}
	}

	// Now send session_shutdown.  The extension's on_session_shutdown handler
	// will call ctx.set_session_data("schedules", ...) — an inbound RPC to us.
	send1(map[string]any{"jsonrpc": "2.0", "method": "event/session_shutdown", "params": nil})

	// Collect until we see set_session_data, servicing any set_status calls.
	var savedScheduleData string
	deadline2 := time.After(5 * time.Second)
	gotSetData := false
	for !gotSetData {
		ch := make(chan jrpcMsg, 1)
		go func() { ch <- recv1() }()
		select {
		case m := <-ch:
			switch m.Method {
			case "set_session_data":
				var p struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				}
				json.Unmarshal(m.Params, &p)
				t.Logf("set_session_data: key=%q value=%q", p.Key, p.Value)
				if p.Key != "schedules" {
					t.Errorf("expected key=schedules, got %q", p.Key)
				}
				savedScheduleData = p.Value
				serviceOK(send1, m)
				gotSetData = true
			case "set_status":
				serviceOK(send1, m)
			default:
				serviceOK(send1, m)
			}
		case <-deadline2:
			t.Fatal("timeout: extension never called set_session_data in session_shutdown handler.\n" +
				"This likely means session_shutdown was not emitted before CollectSessionData() was called (the /reexec ordering bug).")
		}
	}

	if savedScheduleData == "" {
		t.Fatal("set_session_data was called with empty value")
	}
	// The saved data should be valid JSON containing at least one schedule entry.
	var schedules []map[string]any
	if err := json.Unmarshal([]byte(savedScheduleData), &schedules); err != nil {
		t.Fatalf("saved schedule data is not valid JSON: %v\ndata: %s", err, savedScheduleData)
	}
	if len(schedules) == 0 {
		t.Fatal("saved schedule data contains no entries")
	}
	t.Logf("✓ Phase 1: schedule saved on shutdown: %d entry/entries", len(schedules))

	kill1()

	// -----------------------------------------------------------------------
	// Phase 2: new process (simulating post-reexec), restore via session_start
	// -----------------------------------------------------------------------
	send2, recv2, kill2 := spawnProc(t)
	defer kill2()

	send2(map[string]any{"jsonrpc": "2.0", "id": id(), "method": "init",
		"params": map[string]any{"version": "1", "cwd": t.TempDir()}})
	recv2() // consume init response

	// Fire session_start with the saved schedule data — mirrors what
	// EmitSessionStartWithData does after a /reexec.
	send2(map[string]any{
		"jsonrpc": "2.0",
		"method":  "event/session_start",
		"params": map[string]any{
			"session_data": map[string]string{"schedules": savedScheduleData},
		},
	})

	// The extension calls _restore_schedules() then _update_status(), which
	// issues a set_status RPC with "⏰ [s1] ..." — collect until we see it.
	deadline3 := time.After(8 * time.Second)
	gotStatus := false
	for !gotStatus {
		ch := make(chan jrpcMsg, 1)
		go func() { ch <- recv2() }()
		select {
		case m := <-ch:
			t.Logf("phase2 msg: method=%q params=%s", m.Method, m.Params)
			if m.Method == "set_status" {
				var p struct {
					Status string `json:"status"`
				}
				json.Unmarshal(m.Params, &p)
				serviceOK(send2, m)
				if strings.Contains(p.Status, "⏰") {
					t.Logf("✓ Phase 2: schedule status restored after reexec: %q", p.Status)
					gotStatus = true
				}
			} else {
				serviceOK(send2, m)
			}
		case <-deadline3:
			t.Fatal("timeout: extension never emitted ⏰ status after session_start with schedule data.\n" +
				"This means the schedule was not restored from session_data after reexec.")
		}
	}
}
