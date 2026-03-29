package integration

// Tests for demo.py — exercises the full JSON-RPC protocol between Go and
// the demo extension. Every outbound method call and every event is covered.
//
// Unlike the unit tests (pkg/extension/sdk/python/demo_ext_test.py), this file
// drives the real Python process through the actual extension.Process / Bridge
// stack, so it is a genuine end-to-end integration test.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

// ---- shared helper types --------------------------------------------------

// recorded holds everything seen by the mock BridgeAPI during a test run.
type recorded struct {
	mu           sync.Mutex
	notifies     []notifyCall
	statuses     []string
	sessionNames []string
	labels       map[string]string // entry_id → label ("" = cleared)
	models       []modelCall
	messages     []msgCall
	userMessages []string
	execs        []execCall
}

type notifyCall struct{ Level, Message string }
type modelCall struct{ Provider, ID string }
type msgCall struct {
	CustomType string
	Content    any
}
type execCall struct {
	Command string
	Args    []string
}

func newRecorded() *recorded { return &recorded{labels: make(map[string]string)} }

// --------------------------------------------------------------------------
// Raw-protocol helpers — drive the extension process directly over JSON
// --------------------------------------------------------------------------

type jrpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// demoProc wraps a running demo.py process with a background pump goroutine
// that continuously reads stdout. Outbound requests from the extension are
// auto-responded to (and recorded); responses are routed to callers via
// per-ID channels.
type demoProc struct {
	t      *testing.T
	enc    *json.Encoder
	rec    *recorded
	nextID int

	// channels for async recv
	mu      sync.Mutex
	waiters map[int]chan jrpcMsg
}

func (d *demoProc) send(msg any) {
	d.t.Helper()
	if err := d.enc.Encode(msg); err != nil {
		d.t.Fatalf("send: %v", err)
	}
}

// handleOutbound processes an outbound API call from the extension and sends
// the expected response. All calls are recorded on d.rec.
//
// The mutex is released before the pipe write so that waitOutbound's spin loop
// can acquire d.rec.mu while the write is in progress.
func (d *demoProc) handleOutbound(msg jrpcMsg) {
	id := *msg.ID

	// Record the call while holding the mutex, then release before the pipe
	// write so that waitOutbound's spin loop is never locked out.
	d.rec.mu.Lock()
	var result any

	switch msg.Method {
	case "notify":
		var p struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.notifies = append(d.rec.notifies, notifyCall{p.Level, p.Message})
		result = map[string]any{"ok": true}

	case "set_status":
		var p struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.statuses = append(d.rec.statuses, p.Status)
		result = map[string]any{"ok": true}

	case "set_session_name":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.sessionNames = append(d.rec.sessionNames, p.Name)
		result = map[string]any{"ok": true}

	case "set_label":
		var p struct {
			EntryID string `json:"entry_id"`
			Label   string `json:"label"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.labels[p.EntryID] = p.Label
		result = map[string]any{"ok": true}

	case "clear_label":
		var p struct {
			EntryID string `json:"entry_id"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.labels[p.EntryID] = ""
		result = map[string]any{"ok": true}

	case "set_model":
		var p struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.models = append(d.rec.models, modelCall{p.Provider, p.ID})
		result = map[string]any{"ok": true}

	case "send_message":
		var p struct {
			CustomType string `json:"custom_type"`
			Content    any    `json:"content"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.messages = append(d.rec.messages, msgCall{p.CustomType, p.Content})
		result = map[string]any{"ok": true}

	case "send_user_message":
		var p struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.userMessages = append(d.rec.userMessages, p.Content)
		result = map[string]any{"ok": true}

	case "exec":
		var p struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		d.rec.execs = append(d.rec.execs, execCall{p.Command, p.Args})
		result = map[string]any{"stdout": "ok", "stderr": "", "exit_code": 0}

	case "list_tools":
		// Return some mock tool infos.
		result = []map[string]any{
			{"name": "bash", "description": "test tool bash"},
			{"name": "read", "description": "test tool read"},
			{"name": "write", "description": "test tool write"},
		}

	default:
		d.t.Logf("unhandled outbound method: %s", msg.Method)
		result = map[string]any{"ok": true}
	}

	d.rec.mu.Unlock() // release before pipe write

	d.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// pump reads all messages from the extension's stdout in a background goroutine.
// Outbound requests are handled immediately; responses are dispatched to waiters.
func (d *demoProc) pump(scanner *bufio.Scanner) {
	for scanner.Scan() {
		var msg jrpcMsg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			d.t.Logf("pump unmarshal: %v (raw=%s)", err, scanner.Text())
			continue
		}

		if msg.Method != "" && msg.ID != nil {
			// Outbound request from extension → respond and record.
			d.handleOutbound(msg)
			continue
		}

		if msg.Method != "" {
			// Notification — nothing to do.
			continue
		}

		// Response → route to waiter.
		if msg.ID != nil {
			d.mu.Lock()
			ch, ok := d.waiters[*msg.ID]
			d.mu.Unlock()
			if ok {
				ch <- msg
			}
		}
	}
}

// recv waits for the response to the given request ID.
func (d *demoProc) recv(wantID int, timeout time.Duration) (jrpcMsg, bool) {
	ch := make(chan jrpcMsg, 1)
	d.mu.Lock()
	d.waiters[wantID] = ch
	d.mu.Unlock()

	select {
	case msg := <-ch:
		d.mu.Lock()
		delete(d.waiters, wantID)
		d.mu.Unlock()
		return msg, true
	case <-time.After(timeout):
		d.mu.Lock()
		delete(d.waiters, wantID)
		d.mu.Unlock()
		return jrpcMsg{}, false
	}
}

func (d *demoProc) nextid() int {
	d.nextID++
	return d.nextID
}

// sendEvent sends a fire-and-forget notification.
func (d *demoProc) sendEvent(method string, params any) {
	d.send(map[string]any{"jsonrpc": "2.0", "method": "event/" + method, "params": params})
}

// callTool sends a tool_call request and returns the response.
func (d *demoProc) callTool(name string, params map[string]any) jrpcMsg {
	id := d.nextid()
	d.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tool_call",
		"params": map[string]any{
			"tool_call_id": "tc-" + name,
			"name":         name,
			"params":       params,
		},
	})
	msg, ok := d.recv(id, 5*time.Second)
	if !ok {
		d.t.Fatalf("callTool %q: timed out", name)
	}
	return msg
}

// callHook sends a hook request and returns the response.
func (d *demoProc) callHook(hookMethod string, params map[string]any) jrpcMsg {
	id := d.nextid()
	d.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  hookMethod,
		"params":  params,
	})
	msg, ok := d.recv(id, 5*time.Second)
	if !ok {
		d.t.Fatalf("callHook %q: timed out", hookMethod)
	}
	return msg
}

// waitOutbound polls rec until cond returns true, or times out.
func waitOutbound(t *testing.T, rec *recorded, cond func(*recorded) bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		ok := cond(rec)
		rec.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("timed out waiting for outbound call")
}

// --------------------------------------------------------------------------
// Test setup
// --------------------------------------------------------------------------

func startDemo(t *testing.T) (*demoProc, context.CancelFunc) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	sdkDir, _ := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "sdk", "python"))
	if _, err := os.Stat(filepath.Join(sdkDir, "fir_ext.py")); err != nil {
		t.Fatalf("fir_ext.py not found at %s", sdkDir)
	}

	_, repoFile, _, _ := runtime.Caller(0)
	repoRoot, _ := filepath.Abs(filepath.Join(filepath.Dir(repoFile), "..", "..", ".."))
	demoScript := filepath.Join(repoRoot, ".fir", "extensions", "demo.py")
	if _, err := os.Stat(demoScript); err != nil {
		t.Fatalf("demo.py not found at %s", demoScript)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	cmd := exec.CommandContext(ctx, "python3", demoScript)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+sdkDir)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	rec := newRecorded()

	proc := &demoProc{
		t:       t,
		enc:     json.NewEncoder(stdin),
		rec:     rec,
		nextID:  1,
		waiters: make(map[int]chan jrpcMsg),
	}

	// Start the background pump goroutine.
	scanner := bufio.NewScanner(stdout)
	go proc.pump(scanner)

	return proc, cancel
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestDemo_Init(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()

	const initID = 0
	proc.send(map[string]any{
		"jsonrpc": "2.0", "id": initID, "method": "init",
		"params": map[string]any{"version": "1", "cwd": "/tmp"},
	})
	resp, ok := proc.recv(initID, 5*time.Second)
	if !ok {
		t.Fatal("init: timed out")
	}
	if resp.Error != nil {
		t.Fatalf("init error: %s", resp.Error)
	}

	var result struct {
		Name  string `json:"name"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse init: %v", err)
	}

	if result.Name != "demo" {
		t.Errorf("name = %q, want demo", result.Name)
	}

	wantTools := []string{"word_count", "shell_run", "change_model", "inject_message"}
	gotTools := make([]string, len(result.Tools))
	for i, ti := range result.Tools {
		gotTools[i] = ti.Name
	}
	for _, wt := range wantTools {
		if !slices.Contains(gotTools, wt) {
			t.Errorf("missing tool %q; got %v", wt, gotTools)
		}
	}

	wantEvents := []string{
		"session_start", "session_shutdown",
		"agent_start", "agent_end",
		"turn_start", "turn_end",
		"message_start", "message_end",
		"tool_execution_start", "tool_execution_end",
		"hook/tool_call",
	}
	for _, we := range wantEvents {
		if !slices.Contains(result.Events, we) {
			t.Errorf("missing event %q; got %v", we, result.Events)
		}
	}
	t.Logf("init OK: %s, tools=%v, events=%v", result.Name, gotTools, result.Events)
}

// doInit sends a standard init and waits for the response.
// It uses a fixed ID (0) that does not consume a slot from d.nextID, so
// subsequent callTool/callHook invocations start cleanly from 2.
func doInit(proc *demoProc) {
	const initID = 0
	proc.send(map[string]any{
		"jsonrpc": "2.0", "id": initID, "method": "init",
		"params": map[string]any{"version": "1", "cwd": "/tmp"},
	})
	if _, ok := proc.recv(initID, 5*time.Second); !ok {
		proc.t.Fatal("doInit: timed out")
	}
}

// ---- Tools ----------------------------------------------------------------

func TestDemo_Tool_WordCount(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callTool("word_count", map[string]any{"text": "hello world foo"})
	if resp.Error != nil {
		t.Fatalf("word_count error: %s", resp.Error)
	}
	var r map[string]any
	_ = json.Unmarshal(resp.Result, &r)
	if count, _ := r["count"].(float64); int(count) != 3 {
		t.Errorf("count = %v, want 3", r["count"])
	}

	// word_count calls set_label and notify
	waitOutbound(t, proc.rec, func(r *recorded) bool { return r.labels["last_wc"] == "3" }, 3*time.Second)
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.notifies) > 0 }, 3*time.Second)

	proc.rec.mu.Lock()
	if label := proc.rec.labels["last_wc"]; label != "3" {
		t.Errorf("set_label last_wc = %q, want 3", label)
	}
	if len(proc.rec.notifies) == 0 {
		t.Error("expected notify call")
	} else if msg := proc.rec.notifies[0].Message; msg == "" {
		t.Error("notify message empty")
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Tool_ShellRun(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callTool("shell_run", map[string]any{"command": "echo", "args": []string{"hi"}})
	if resp.Error != nil {
		t.Fatalf("shell_run error: %s", resp.Error)
	}
	// shell_run calls exec — the pump already handled it; verify the record
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.execs) > 0 }, 3*time.Second)

	proc.rec.mu.Lock()
	if len(proc.rec.execs) == 0 {
		t.Error("expected exec call")
	} else {
		ex := proc.rec.execs[0]
		if ex.Command != "echo" {
			t.Errorf("exec command = %q, want echo", ex.Command)
		}
		if !slices.Contains(ex.Args, "hi") {
			t.Errorf("exec args = %v, want [hi]", ex.Args)
		}
	}
	// The tool returns the exec result
	var r map[string]any
	_ = json.Unmarshal(resp.Result, &r)
	if r["stdout"] == nil {
		t.Error("shell_run result missing stdout")
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Tool_ChangeModel(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callTool("change_model", map[string]any{"provider": "anthropic", "model": "claude-3"})
	if resp.Error != nil {
		t.Fatalf("change_model error: %s", resp.Error)
	}
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.models) > 0 }, 3*time.Second)

	proc.rec.mu.Lock()
	if len(proc.rec.models) == 0 {
		t.Error("expected set_model call")
	} else {
		m := proc.rec.models[0]
		if m.Provider != "anthropic" || m.ID != "claude-3" {
			t.Errorf("model = %+v, want anthropic/claude-3", m)
		}
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Tool_InjectMessage_Custom(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callTool("inject_message", map[string]any{"kind": "custom", "content": "hello"})
	if resp.Error != nil {
		t.Fatalf("inject_message error: %s", resp.Error)
	}
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.messages) > 0 }, 3*time.Second)

	proc.rec.mu.Lock()
	if len(proc.rec.messages) == 0 {
		t.Error("expected send_message call")
	} else {
		m := proc.rec.messages[0]
		if m.CustomType != "demo_note" {
			t.Errorf("custom_type = %q, want demo_note", m.CustomType)
		}
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Tool_InjectMessage_User(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callTool("inject_message", map[string]any{"kind": "user", "content": "hi agent"})
	if resp.Error != nil {
		t.Fatalf("inject_message error: %s", resp.Error)
	}
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.userMessages) > 0 }, 3*time.Second)

	proc.rec.mu.Lock()
	if len(proc.rec.userMessages) == 0 {
		t.Error("expected send_user_message call")
	} else if proc.rec.userMessages[0] != "hi agent" {
		t.Errorf("user message = %q, want 'hi agent'", proc.rec.userMessages[0])
	}
	proc.rec.mu.Unlock()
}

// ---- Hook -----------------------------------------------------------------

func TestDemo_Hook_BlockedTool(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callHook("hook/tool_call", map[string]any{"tool_name": "blocked:dangerous"})
	if resp.Error != nil {
		t.Fatalf("hook error: %s", resp.Error)
	}
	var r map[string]any
	_ = json.Unmarshal(resp.Result, &r)
	if block, _ := r["block"].(bool); !block {
		t.Errorf("block = %v, want true; full result: %s", r["block"], resp.Result)
	}
	if reason, _ := r["reason"].(string); reason == "" {
		t.Error("hook returned empty reason")
	}
}

func TestDemo_Hook_AllowedTool(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	resp := proc.callHook("hook/tool_call", map[string]any{"tool_name": "bash"})
	if resp.Error != nil {
		t.Fatalf("hook error: %s", resp.Error)
	}
	// should return null (no blocking)
	if string(resp.Result) != "null" {
		t.Errorf("expected null result for allowed tool, got %s", resp.Result)
	}
}

// ---- Events ---------------------------------------------------------------

func TestDemo_Event_SessionStart(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	proc.sendEvent("session_start", nil)

	waitOutbound(t, proc.rec, func(r *recorded) bool {
		return slices.Contains(r.statuses, "demo ready")
	}, 5*time.Second)

	proc.rec.mu.Lock()
	if !slices.Contains(proc.rec.statuses, "demo ready") {
		t.Errorf("statuses = %v, want 'demo ready'", proc.rec.statuses)
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Event_SessionShutdown(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	proc.sendEvent("session_shutdown", nil)

	waitOutbound(t, proc.rec, func(r *recorded) bool {
		return slices.Contains(r.statuses, "")
	}, 5*time.Second)

	proc.rec.mu.Lock()
	if !slices.Contains(proc.rec.statuses, "") {
		t.Errorf("set_status('') not seen; statuses = %v", proc.rec.statuses)
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Event_AgentStart(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	proc.sendEvent("agent_start", nil)

	waitOutbound(t, proc.rec, func(r *recorded) bool {
		return slices.Contains(r.sessionNames, "demo session")
	}, 5*time.Second)

	proc.rec.mu.Lock()
	if !slices.Contains(proc.rec.sessionNames, "demo session") {
		t.Errorf("set_session_name not called; got %v", proc.rec.sessionNames)
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Event_AgentEnd(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	// Prime last_wc so the clear_label call targets a known key.
	proc.callTool("word_count", map[string]any{"text": "a b c"})
	waitOutbound(t, proc.rec, func(r *recorded) bool { return r.labels["last_wc"] == "3" }, 3*time.Second)

	proc.sendEvent("agent_end", nil)

	// agent_end should call notify and clear_label("last_wc")
	waitOutbound(t, proc.rec, func(r *recorded) bool { return len(r.notifies) > 0 }, 5*time.Second)
	waitOutbound(t, proc.rec, func(r *recorded) bool { return r.labels["last_wc"] == "" }, 5*time.Second)

	proc.rec.mu.Lock()
	if len(proc.rec.notifies) == 0 {
		t.Error("expected notify after agent_end")
	}
	if proc.rec.labels["last_wc"] != "" {
		t.Errorf("expected last_wc cleared, got %q", proc.rec.labels["last_wc"])
	}
	proc.rec.mu.Unlock()
}

func TestDemo_Event_ToolExecutionStartEnd(t *testing.T) {
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	proc.sendEvent("tool_execution_start", map[string]any{
		"tool_call_id": "tc-42",
		"tool_name":    "bash",
	})
	waitOutbound(t, proc.rec, func(r *recorded) bool { return r.labels["tc-42"] != "" }, 5*time.Second)

	proc.rec.mu.Lock()
	if label := proc.rec.labels["tc-42"]; label == "" {
		t.Error("expected set_label after tool_execution_start")
	} else {
		t.Logf("set_label: %q", label)
	}
	proc.rec.mu.Unlock()

	proc.sendEvent("tool_execution_end", map[string]any{
		"tool_call_id": "tc-42",
		"tool_name":    "bash",
		"is_error":     false,
	})
	waitOutbound(t, proc.rec, func(r *recorded) bool { return r.labels["tc-42"] == "" }, 5*time.Second)

	proc.rec.mu.Lock()
	if proc.rec.labels["tc-42"] != "" {
		t.Errorf("expected clear_label after tool_execution_end, got %q", proc.rec.labels["tc-42"])
	}
	proc.rec.mu.Unlock()
}

// No-action events: subscribed but produce no outbound call.
// We verify that after each event the process is still responsive.

func testNoActionEvent(t *testing.T, eventName string) {
	t.Helper()
	proc, cancel := startDemo(t)
	defer cancel()
	doInit(proc)

	proc.sendEvent(eventName, nil)
	time.Sleep(50 * time.Millisecond)

	// Extension should still be alive — a subsequent tool call must succeed.
	resp := proc.callTool("word_count", map[string]any{"text": "hello"})
	if resp.Error != nil {
		t.Errorf("process unresponsive after event %q: %s", eventName, resp.Error)
	}
}

func TestDemo_Event_TurnStart(t *testing.T)    { testNoActionEvent(t, "turn_start") }
func TestDemo_Event_TurnEnd(t *testing.T)      { testNoActionEvent(t, "turn_end") }
func TestDemo_Event_MessageStart(t *testing.T) { testNoActionEvent(t, "message_start") }
func TestDemo_Event_MessageEnd(t *testing.T)   { testNoActionEvent(t, "message_end") }
