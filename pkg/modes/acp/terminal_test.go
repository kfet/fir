package acp

import (
	"context"
	"fmt"
	"testing"
)

func TestNewTerminalState(t *testing.T) {
	ts := newTerminalState()
	if ts == nil {
		t.Fatal("expected non-nil terminal state")
	}
	if len(ts.pendingBashTerminals) != 0 {
		t.Error("expected empty pending terminals")
	}
	if len(ts.backgroundTerminals) != 0 {
		t.Error("expected empty background terminals")
	}
}

func TestTerminalState_PendingTracking(t *testing.T) {
	ts := newTerminalState()

	// Store a pending terminal
	ts.mu.Lock()
	ts.pendingBashTerminals["tool1"] = "term1"
	ts.mu.Unlock()

	// Check presence
	ts.mu.Lock()
	_, ok := ts.pendingBashTerminals["tool1"]
	ts.mu.Unlock()
	if !ok {
		t.Error("expected pending terminal for tool1")
	}

	// Remove it
	ts.mu.Lock()
	delete(ts.pendingBashTerminals, "tool1")
	ts.mu.Unlock()

	ts.mu.Lock()
	_, ok = ts.pendingBashTerminals["tool1"]
	ts.mu.Unlock()
	if ok {
		t.Error("expected no pending terminal for tool1 after delete")
	}
}

func TestTerminalState_BackgroundTracking(t *testing.T) {
	ts := newTerminalState()

	// Add background terminals up to limit
	ts.mu.Lock()
	for i := 0; i < maxBackgroundTerminals; i++ {
		ts.backgroundTerminals[string(rune('a'+i))] = string(rune('a' + i))
	}
	count := len(ts.backgroundTerminals)
	ts.mu.Unlock()

	if count != maxBackgroundTerminals {
		t.Errorf("expected %d background terminals, got %d", maxBackgroundTerminals, count)
	}
}

func TestAcpBashExec_Success(t *testing.T) {
	mc := newMockConn()
	mc.terminalOutput = "hello\n"
	exitCode := 0
	mc.terminalExit = &exitCode

	ts := newTerminalState()
	result, err := AcpBashExec(context.Background(), mc, ts, "s1", "tc1", "echo hello", "/tmp", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "hello\n" {
		t.Errorf("output = %q, want %q", result.Output, "hello\n")
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", result.ExitCode)
	}
}

func TestAcpBashExec_Cancelled(t *testing.T) {
	mc := newMockConn()
	ts := newTerminalState()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := AcpBashExec(ctx, mc, ts, "s1", "tc1", "sleep 10", "/tmp", 0)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestAcpBashExec_WaitError(t *testing.T) {
	mc := newMockConn()
	mc.waitError = fmt.Errorf("connection lost")
	ts := newTerminalState()

	_, err := AcpBashExec(context.Background(), mc, ts, "s1", "tc1", "echo hi", "/tmp", 0)
	if err == nil {
		t.Fatal("expected error from WaitForTerminalExit")
	}
	if err.Error() != "connection lost" {
		t.Errorf("err = %q, want %q", err.Error(), "connection lost")
	}
	// pendingBashTerminals must be cleaned up.
	ts.mu.Lock()
	pending := len(ts.pendingBashTerminals)
	ts.mu.Unlock()
	if pending != 0 {
		t.Errorf("pendingBashTerminals has %d entries after error, want 0", pending)
	}
}

func TestStartBackgroundCommand_Success(t *testing.T) {
	mc := newMockConn()
	mc.nextTerminalID = "bg-term-1"
	ts := newTerminalState()

	cmdID, err := StartBackgroundCommand(context.Background(), mc, ts, "s1", "sleep 10", "/tmp", "tc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmdID != "bg-term-1" {
		t.Errorf("cmdID = %q, want bg-term-1", cmdID)
	}

	// Check it's tracked
	ts.mu.Lock()
	_, ok := ts.backgroundTerminals["bg-term-1"]
	ts.mu.Unlock()
	if !ok {
		t.Error("expected background terminal to be tracked")
	}
}

func TestStartBackgroundCommand_AtLimit(t *testing.T) {
	mc := newMockConn()
	ts := newTerminalState()

	// Fill to max
	for i := 0; i < maxBackgroundTerminals; i++ {
		ts.backgroundTerminals[string(rune('a'+i))] = string(rune('a' + i))
	}

	_, err := StartBackgroundCommand(context.Background(), mc, ts, "s1", "echo hi", "/tmp", "tc1")
	if err == nil {
		t.Fatal("expected error at max background terminals")
	}
}

func TestGetBackgroundOutput_NotFound(t *testing.T) {
	mc := newMockConn()
	ts := newTerminalState()

	_, _, _, err := GetBackgroundOutput(context.Background(), mc, ts, "s1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestGetBackgroundOutput_Running(t *testing.T) {
	mc := newMockConn()
	mc.terminalOutput = "partial output"
	mc.terminalExit = nil // still running
	ts := newTerminalState()
	ts.backgroundTerminals["cmd1"] = "term1"

	output, isRunning, exitCode, err := GetBackgroundOutput(context.Background(), mc, ts, "s1", "cmd1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "partial output" {
		t.Errorf("output = %q", output)
	}
	if !isRunning {
		t.Error("expected isRunning = true")
	}
	if exitCode != nil {
		t.Errorf("expected nil exit code, got %v", exitCode)
	}
}

func TestKillBackgroundCommand_NotFound(t *testing.T) {
	mc := newMockConn()
	ts := newTerminalState()

	_, _, err := KillBackgroundCommand(context.Background(), mc, ts, "s1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestKillBackgroundCommand_Success(t *testing.T) {
	mc := newMockConn()
	mc.terminalOutput = "final output"
	exitCode := 137
	mc.terminalExit = &exitCode
	ts := newTerminalState()
	ts.backgroundTerminals["cmd1"] = "term1"

	output, ec, err := KillBackgroundCommand(context.Background(), mc, ts, "s1", "cmd1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "final output" {
		t.Errorf("output = %q", output)
	}
	if ec == nil || *ec != 137 {
		t.Errorf("exit code = %v, want 137", ec)
	}

	// Verify cleanup
	ts.mu.Lock()
	_, ok := ts.backgroundTerminals["cmd1"]
	ts.mu.Unlock()
	if ok {
		t.Error("expected background terminal to be removed after kill")
	}
}

func TestCleanupBackgroundTerminals(t *testing.T) {
	mc := newMockConn()
	ts := newTerminalState()
	ts.backgroundTerminals["cmd1"] = "term1"
	ts.backgroundTerminals["cmd2"] = "term2"

	CleanupBackgroundTerminals(context.Background(), mc, ts, "s1")

	ts.mu.Lock()
	count := len(ts.backgroundTerminals)
	ts.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 background terminals after cleanup, got %d", count)
	}
}
