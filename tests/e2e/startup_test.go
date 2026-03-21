//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestStartup_InteractivePromptAppearsQuickly launches fir in interactive mode
// with a slow extension and asserts the TUI prompt renders within 3s.
// This guards against regressions where extension setup blocks the TUI.
func TestStartup_InteractivePromptAppearsQuickly(t *testing.T) {
	// Create a project dir with a deliberately slow extension.
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	os.MkdirAll(extDir, 0o755)
	// Extension that sleeps 5s during init — if fir blocks on this,
	// the test will fail the deadline.
	os.WriteFile(filepath.Join(extDir, "slow_blocker.py"), []byte(`#!/usr/bin/env python3
# name: slow-blocker
# description: deliberately slow extension for startup test
import sys, time, json

# Sleep to simulate slow startup. If fir blocks on this, the TUI won't
# render within the deadline.
time.sleep(5)

# Minimal JSON-RPC handshake
line = sys.stdin.readline()
req = json.loads(line)
resp = {"jsonrpc": "2.0", "id": req["id"], "result": {"name": "slow-blocker", "version": "1"}}
sys.stdout.write(json.dumps(resp) + "\n")
sys.stdout.flush()

# Keep alive
while True:
    time.sleep(1)
`), 0o755)

	cmd := exec.Command(firBinary,
		"--provider", "mock",
		"--model", "mock-model",
		"--no-auto-update",
	)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		"FIR_AGENT_DIR="+mockAgentDir,
		"TERM=xterm-256color",
	)

	// Start in a PTY so fir thinks it's interactive and renders the TUI.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("failed to start fir in pty: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		ptmx.Close()
	}()

	// Read output until we see the prompt or timeout.
	deadline := time.After(3 * time.Second)
	buf := make([]byte, 0, 16384)
	chunk := make([]byte, 4096)
	found := false

	readCh := make(chan []byte, 64)
	errCh := make(chan error, 1)
	go func() {
		for {
			n, err := ptmx.Read(chunk)
			if n > 0 {
				data := make([]byte, n)
				copy(data, chunk[:n])
				readCh <- data
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	for !found {
		select {
		case <-deadline:
			t.Fatalf("TUI prompt did not appear within 3s.\nOutput so far (%d bytes): %q", len(buf), string(buf))
		case data := <-readCh:
			buf = append(buf, data...)
			s := string(buf)
			if strings.Contains(s, "enter submit") || strings.Contains(s, "commands") {
				found = true
			}
		case err := <-errCh:
			t.Fatalf("pty closed before prompt appeared: %v\nOutput: %q", err, string(buf))
		}
	}

	t.Logf("TUI prompt appeared, output length: %d bytes", len(buf))
}
