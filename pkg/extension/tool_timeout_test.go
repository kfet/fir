package extension

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// TestResolveToolCallTimeout covers the three declared-timeout semantics
// (0 / >0 / <0) plus the FIR_EXT_TOOL_TIMEOUT env override for the default.
func TestResolveToolCallTimeout(t *testing.T) {
	t.Run("zero uses default", func(t *testing.T) {
		t.Setenv("FIR_EXT_TOOL_TIMEOUT", "")
		if got := resolveToolCallTimeout(0); got != DefaultToolCallTimeout {
			t.Fatalf("declared 0: got %v, want %v", got, DefaultToolCallTimeout)
		}
	})

	t.Run("positive uses that many seconds", func(t *testing.T) {
		if got := resolveToolCallTimeout(5); got != 5*time.Second {
			t.Fatalf("declared 5: got %v, want 5s", got)
		}
		if got := resolveToolCallTimeout(0.05); got != 50*time.Millisecond {
			t.Fatalf("declared 0.05: got %v, want 50ms", got)
		}
	})

	t.Run("negative disables (non-positive)", func(t *testing.T) {
		if got := resolveToolCallTimeout(-1); got > 0 {
			t.Fatalf("declared -1: got %v, want <= 0 (disabled)", got)
		}
	})

	t.Run("env overrides default only", func(t *testing.T) {
		t.Setenv("FIR_EXT_TOOL_TIMEOUT", "7")
		if got := resolveToolCallTimeout(0); got != 7*time.Second {
			t.Fatalf("env override: got %v, want 7s", got)
		}
		// Explicit positive/negative declarations ignore the env override.
		if got := resolveToolCallTimeout(3); got != 3*time.Second {
			t.Fatalf("explicit positive with env set: got %v, want 3s", got)
		}
		if got := resolveToolCallTimeout(-1); got > 0 {
			t.Fatalf("explicit negative with env set: got %v, want disabled", got)
		}
	})

	t.Run("invalid env ignored", func(t *testing.T) {
		t.Setenv("FIR_EXT_TOOL_TIMEOUT", "not-a-number")
		if got := resolveToolCallTimeout(0); got != DefaultToolCallTimeout {
			t.Fatalf("invalid env: got %v, want default", got)
		}
		t.Setenv("FIR_EXT_TOOL_TIMEOUT", "-4")
		if got := resolveToolCallTimeout(0); got != DefaultToolCallTimeout {
			t.Fatalf("non-positive env: got %v, want default", got)
		}
	})
}

// TestBridge_RegisterTools_PositiveTimeout verifies that a tool declaring a
// small positive Timeout is clipped by the host when the extension stays
// silent (no activity to extend the deadline).
func TestBridge_RegisterTools_PositiveTimeout(t *testing.T) {
	caps := &InitResult{
		Tools: []ToolSpec{
			{Name: "slow_tool", Timeout: 0.05}, // 50ms
		},
	}
	b, extCodec := pipePair(caps)
	api := newMockAPI()
	b.RegisterTools(api)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Extension reads the tool_call request but never responds — silent.
	go func() {
		for {
			if _, err := extCodec.ReadMessage(); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	res, err := api.toolsRegistered[0].Execute(ToolContext{
		Context:    context.Background(),
		ToolCallID: "tc1",
		Params:     map[string]any{},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected is_error result on host-side timeout")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "timed out") {
		t.Fatalf("expected 'timed out' text, got %+v", res.Content)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v (declared 50ms)", elapsed)
	}
}

// TestBridge_RegisterTools_DisabledTimeout verifies that a tool declaring a
// negative Timeout applies NO host-side deadline: a silent extension is not
// clipped by any hook timeout, and the call ends only when the turn context
// is cancelled.
func TestBridge_RegisterTools_DisabledTimeout(t *testing.T) {
	caps := &InitResult{
		Tools: []ToolSpec{
			{Name: "long_tool", Timeout: -1}, // disabled
		},
	}
	b, extCodec := pipePair(caps)
	api := newMockAPI()
	b.RegisterTools(api)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() { _ = b.Run(runCtx, api) }()

	// Extension stays silent — never responds.
	go func() {
		for {
			if _, err := extCodec.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// The tool call is bounded only by its own context. Give it a deadline
	// far shorter than any default timeout so we can assert it ends via ctx,
	// not via a host-side hook timeout.
	callCtx, callCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer callCancel()

	start := time.Now()
	res, err := api.toolsRegistered[0].Execute(ToolContext{
		Context:    callCtx,
		ToolCallID: "tc1",
		Params:     map[string]any{},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected is_error result when context deadline hits")
	}
	// It must be a context error, NOT a "hook ... timed out after" host timeout.
	txt := ""
	if len(res.Content) > 0 {
		txt = res.Content[0].Text
	}
	if strings.Contains(txt, "timed out after") {
		t.Fatalf("disabled timeout must not fire a host-side deadline, got: %q", txt)
	}
	if !strings.Contains(txt, "context") {
		t.Fatalf("expected a context error, got: %q", txt)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("ended too early (%v) — did the host apply a deadline?", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ended too late: %v", elapsed)
	}
}

// TestBridge_RegisterTools_DisabledTimeout_FailsOnClose verifies that a tool
// with a disabled host-side timeout does not hang forever when the extension
// stream closes mid-call: failAllPending delivers an error so the call
// returns promptly instead of blocking until the turn context is cancelled.
func TestBridge_RegisterTools_DisabledTimeout_FailsOnClose(t *testing.T) {
	// Build a bridge with pipes we control so we can close the bridge's read
	// source (simulating the extension process crashing).
	extR, firW := io.Pipe()
	firR, extW := io.Pipe()
	proc := &Process{}
	proc.codec = NewCodec(firR, firW)
	b := NewBridge(proc, &InitResult{
		Tools: []ToolSpec{{Name: "long_tool", Timeout: -1}}, // disabled
	})
	extCodec := NewCodec(extR, extW)

	api := newMockAPI()
	b.RegisterTools(api)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() { _ = b.Run(runCtx, api) }()

	// Extension reads the tool_call request, then closes the stream the bridge
	// reads from (crash → ReadMessage returns EOF → errCh path).
	go func() {
		_, _ = extCodec.ReadMessage()
		_ = extW.Close()
	}()

	done := make(chan ToolResult, 1)
	go func() {
		res, _ := api.toolsRegistered[0].Execute(ToolContext{
			Context:    context.Background(), // never cancelled by the test
			ToolCallID: "tc1",
			Params:     map[string]any{},
		})
		done <- res
	}()

	select {
	case res := <-done:
		if !res.IsError {
			t.Fatal("expected is_error result after stream close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disabled-timeout call hung after stream close (failAllPending not wired?)")
	}
}

// TestBridge_CallHook_FailsAfterClose covers the register-after-close race: a
// CallHook that begins *after* the read loop has already exited (failAllPending
// has run) must fail fast rather than register into the fresh pending map and
// block forever. With a disabled host-side timeout and an uncancelled context
// this would otherwise hang until the user hits ESC.
func TestBridge_CallHook_FailsAfterClose(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	// Drain the extension side so a stray WriteRequest never blocks the test.
	go func() {
		for {
			if _, err := extCodec.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Simulate the read loop having exited (extension crash / EOF).
	b.failAllPending(fmt.Errorf("boom"))

	done := make(chan error, 1)
	go func() {
		// Disabled host-side timeout + uncancelled context: only the closed
		// flag can end this promptly.
		_, err := b.CallHook(context.Background(), "tool_call", map[string]any{}, 0)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from CallHook after close, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallHook after close hung (register-after-close race not closed?)")
	}
}
