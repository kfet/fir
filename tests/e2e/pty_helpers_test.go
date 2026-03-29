//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// FirPTY drives a fir binary inside a real PTY, suitable for testing
// interactive TUI behaviour (prompts, /reexec, status bar, etc.)
// without requiring tmux.
type FirPTY struct {
	t      *testing.T
	cmd    *exec.Cmd
	ptmx   *os.File
	mu     sync.Mutex
	output bytes.Buffer // all output read so far
	done   chan struct{}
}

// StartFirPTY launches fir in a PTY with the given args and env overrides.
// The process is killed on test cleanup.
func StartFirPTY(t *testing.T, dir string, env map[string]string, args ...string) *FirPTY {
	t.Helper()

	allArgs := append([]string{"--provider", "mock", "--model", "mock-model"}, args...)
	cmd := exec.Command(firBinary, allArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FIR_AGENT_DIR="+mockAgentDir, "TERM=xterm-256color")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 220})
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}

	fp := &FirPTY{
		t:    t,
		cmd:  cmd,
		ptmx: ptmx,
		done: make(chan struct{}),
	}

	// Background goroutine to continuously read PTY output.
	go func() {
		defer close(fp.done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				fp.mu.Lock()
				fp.output.Write(buf[:n])
				fp.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		ptmx.Close()
		cmd.Wait()
	})

	return fp
}

// Output returns all PTY output captured so far.
func (fp *FirPTY) Output() string {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.output.String()
}

// Send types a string followed by Enter (carriage return) into the PTY.
// Characters are sent with a small inter-key delay so the TUI's event loop
// can process them reliably (bubbletea reads from the PTY in a goroutine
// and rapid bulk writes can be coalesced into a single Read).
func (fp *FirPTY) Send(input string) {
	fp.t.Helper()
	for _, ch := range input {
		_, err := fp.ptmx.Write([]byte(string(ch)))
		if err != nil {
			fp.t.Logf("FirPTY.Send char %q: %v", ch, err)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Send carriage return (Enter) — terminals send \r, not \n.
	if _, err := fp.ptmx.Write([]byte("\r")); err != nil {
		fp.t.Logf("FirPTY.Send CR: %v", err)
	}
}

// SendRaw writes raw bytes to the PTY (no trailing newline).
func (fp *FirPTY) SendRaw(data string) {
	fp.t.Helper()
	_, err := fp.ptmx.Write([]byte(data))
	if err != nil {
		fp.t.Logf("FirPTY.SendRaw: %v", err)
	}
}

// WaitFor polls the accumulated output for a substring, returning true if
// found within the timeout. This strips ANSI escape sequences for matching.
func (fp *FirPTY) WaitFor(want string, timeout time.Duration) bool {
	fp.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(fp.StrippedOutput(), want) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// WaitForAny waits for any of the given substrings, returning the index of
// the first match or -1 on timeout.
func (fp *FirPTY) WaitForAny(wants []string, timeout time.Duration) int {
	fp.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := fp.StrippedOutput()
		for i, want := range wants {
			if strings.Contains(out, want) {
				return i
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return -1
}

// StrippedOutput returns the captured output with ANSI escape sequences removed.
func (fp *FirPTY) StrippedOutput() string {
	return stripANSI(fp.Output())
}

// SnapshotOutput returns the current stripped output and logs it, useful for
// debugging test failures.
func (fp *FirPTY) SnapshotOutput(label string) string {
	fp.t.Helper()
	out := fp.StrippedOutput()
	fp.t.Logf("[%s] PTY output (%d bytes):\n%s", label, len(out), truncate(out, 2000))
	return out
}

// RequireWaitFor is like WaitFor but fails the test on timeout.
func (fp *FirPTY) RequireWaitFor(want string, timeout time.Duration, msgAndArgs ...any) {
	fp.t.Helper()
	if !fp.WaitFor(want, timeout) {
		fp.SnapshotOutput("timeout waiting for: " + want)
		msg := fmt.Sprintf("FirPTY: %q not found within %s", want, timeout)
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprintf("%s: %s", msg, fmt.Sprint(msgAndArgs...))
		}
		fp.t.Fatal(msg)
	}
}

// WaitForPrompt waits for the fir TUI prompt character "⟩" to appear.
func (fp *FirPTY) WaitForPrompt(timeout time.Duration) {
	fp.t.Helper()
	fp.RequireWaitFor("⟩", timeout, "fir TUI prompt did not appear")
}

// Done returns a channel that's closed when the PTY read loop exits
// (i.e. the process closed its end of the PTY).
func (fp *FirPTY) Done() <-chan struct{} {
	return fp.done
}

// stripANSI removes common ANSI escape sequences from s.
func stripANSI(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			// CSI sequence: ESC [ ... final_byte
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
					j++
				}
				if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
					j++
				}
				i = j
				continue
			}
			// OSC sequence: ESC ] ... ST (or BEL)
			if i+1 < len(s) && s[i+1] == ']' {
				j := i + 2
				for j < len(s) && s[j] != '\x07' {
					if j+1 < len(s) && s[j] == '\x1b' && s[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				if j < len(s) && s[j] == '\x07' {
					j++
				}
				i = j
				continue
			}
			// Other ESC + single char
			i += 2
			continue
		}
		buf.WriteByte(s[i])
		i++
	}
	return buf.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
