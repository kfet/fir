package extension

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestHandshake_Success(t *testing.T) {
	// Use pipes to simulate a process's stdio without spawning a real process.
	cR, cW := io.Pipe() // child reads from cR, fir writes to cW
	fR, fW := io.Pipe() // fir reads from fR, child writes to fW

	// Build a fake Process with a codec wired to the pipes.
	proc := &Process{
		cfg:      ExtProcConfig{Name: "test", Path: "/fake", Scope: "project"},
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	// Simulate the extension side in a goroutine.
	extCodec := NewCodec(cR, fW)
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			t.Errorf("ext read: %v", err)
			return
		}
		req, ok := msg.(*Request)
		if !ok {
			t.Errorf("ext expected *Request, got %T", msg)
			return
		}
		if req.Method != "init" {
			t.Errorf("ext expected method init, got %s", req.Method)
			return
		}

		result := InitResult{
			Name: "my-ext",
			Tools: []ToolSpec{
				{Name: "greet", Description: "Says hello", Parameters: map[string]any{"type": "object"}},
			},
			Events: []string{"session_start", "turn_end"},
		}
		_ = extCodec.WriteResponse(req.ID, result, nil)
	}()

	got, err := Handshake(proc, "/tmp/project", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "my-ext" {
		t.Errorf("name = %q, want my-ext", got.Name)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "greet" {
		t.Errorf("tools = %+v", got.Tools)
	}
	if len(got.Events) != 2 {
		t.Errorf("events = %v", got.Events)
	}
}

func TestHandshake_ErrorResponse(t *testing.T) {
	cR, cW := io.Pipe()
	fR, fW := io.Pipe()

	proc := &Process{
		cfg:      ExtProcConfig{Name: "test", Path: "/fake", Scope: "project"},
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	extCodec := NewCodec(cR, fW)
	go func() {
		msg, _ := extCodec.ReadMessage()
		req := msg.(*Request)
		_ = extCodec.WriteResponse(req.ID, nil, &Error{Code: -32600, Message: "bad init"})
	}()

	_, err := Handshake(proc, "/tmp", nil, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandshake_Timeout(t *testing.T) {
	cR, cW := io.Pipe()
	fR, _ := io.Pipe() // nobody writes to fR → ReadMessage blocks

	proc := &Process{
		cfg:      ExtProcConfig{Name: "slow", Path: "/fake", Scope: "project"},
		stdin:    cW,
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	// Drain the child-read side so WriteRequest doesn't block.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := cR.Read(buf); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	_, err := Handshake(proc, "/tmp", nil, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestHandshake_InitParams(t *testing.T) {
	// Verify InitParams serializes correctly, including config_dirs.
	p := InitParams{Version: "1", Cwd: "/my/project", ConfigDirs: []string{"/my/project/.fir", "/home/user/.config/fir"}}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != "1" || got["cwd"] != "/my/project" {
		t.Errorf("unexpected: %s", data)
	}
	dirs, ok := got["config_dirs"].([]any)
	if !ok || len(dirs) != 2 || dirs[0] != "/my/project/.fir" || dirs[1] != "/home/user/.config/fir" {
		t.Errorf("config_dirs unexpected: %v", got["config_dirs"])
	}
	// Verify omitempty: empty ConfigDirs must not appear in JSON.
	p2 := InitParams{Version: "1", Cwd: "/p"}
	data2, _ := json.Marshal(p2)
	if bytes.Contains(data2, []byte("config_dirs")) {
		t.Errorf("config_dirs should be omitted when empty: %s", data2)
	}
}

func TestHandshake_NilCodec(t *testing.T) {
	proc := &Process{
		cfg: ExtProcConfig{Name: "nil", Path: "/fake", Scope: "project"},
	}
	_, err := Handshake(proc, "/tmp", nil, 0)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
}

func TestValidateCommandName(t *testing.T) {
	valid := []string{"greet", "hello-world", "cmd1", "my-cmd"}
	for _, name := range valid {
		if err := ValidateCommandName(name); err != nil {
			t.Errorf("ValidateCommandName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "Hello", "1abc", "_cmd", "cmd name", "cmd/sub", "CMD"}
	for _, name := range invalid {
		if err := ValidateCommandName(name); err == nil {
			t.Errorf("ValidateCommandName(%q) = nil, want error", name)
		}
	}
}

func TestHandshake_Commands(t *testing.T) {
	cR, cW := io.Pipe()
	fR, fW := io.Pipe()

	proc := &Process{
		cfg:      ExtProcConfig{Name: "test", Path: "/fake", Scope: "project"},
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	extCodec := NewCodec(cR, fW)
	go func() {
		msg, _ := extCodec.ReadMessage()
		req := msg.(*Request)
		result := InitResult{
			Name: "cmd-ext",
			Commands: []CommandSpec{
				{Name: "greet", Description: "Greet someone"},
				{Name: "status", Description: "Show status"},
			},
		}
		_ = extCodec.WriteResponse(req.ID, result, nil)
	}()

	got, err := Handshake(proc, "/tmp", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != 2 {
		t.Fatalf("want 2 commands, got %d", len(got.Commands))
	}
	if got.Commands[0].Name != "greet" || got.Commands[1].Name != "status" {
		t.Errorf("unexpected commands: %+v", got.Commands)
	}
}

func TestHandshake_InvalidCommandName(t *testing.T) {
	cR, cW := io.Pipe()
	fR, fW := io.Pipe()

	proc := &Process{
		cfg:      ExtProcConfig{Name: "test", Path: "/fake", Scope: "project"},
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	extCodec := NewCodec(cR, fW)
	go func() {
		msg, _ := extCodec.ReadMessage()
		req := msg.(*Request)
		result := InitResult{
			Name: "bad-ext",
			Commands: []CommandSpec{
				{Name: "Bad Name!", Description: "Invalid"},
			},
		}
		_ = extCodec.WriteResponse(req.ID, result, nil)
	}()

	_, err := Handshake(proc, "/tmp", nil, 0)
	if err == nil {
		t.Fatal("expected error for invalid command name")
	}
}
