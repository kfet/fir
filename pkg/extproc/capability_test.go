package extproc

import (
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

	got, err := Handshake(proc, "/tmp/project", 0)
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

	_, err := Handshake(proc, "/tmp", 0)
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
	_, err := Handshake(proc, "/tmp", 50*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestHandshake_InitParams(t *testing.T) {
	// Verify InitParams serializes correctly.
	p := InitParams{Version: "1", Cwd: "/my/project"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	json.Unmarshal(data, &got)
	if got["version"] != "1" || got["cwd"] != "/my/project" {
		t.Errorf("unexpected: %s", data)
	}
}

func TestHandshake_NilCodec(t *testing.T) {
	proc := &Process{
		cfg: ExtProcConfig{Name: "nil", Path: "/fake", Scope: "project"},
	}
	_, err := Handshake(proc, "/tmp", 0)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
}
