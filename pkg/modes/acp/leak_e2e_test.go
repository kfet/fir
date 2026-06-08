// ACP session-leak regression test.
//
// This is the e2e guard that maps directly to the production OOM: fir
// --mode acp accumulated one *firSession per ACP session, each spawning
// persistent python extension sidecars, and the map only ever shrank on
// full shutdown. This test spawns a real fir --mode acp subprocess (with the
// binary's builtin python extensions active), creates N sessions — watching
// the child python pid count climb — then proves both teardown paths return
// the pid count to baseline:
//
//   - explicit session/release, and
//   - the idle-session reaper (--acp-session-idle-ttl).
//
// Requires FIR_TEST_BINARY (built fir) and python3 on PATH.
package acp_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// noopAgentHandler is a JSON-RPC handler for the client side of the raw
// connection. The agent under test never calls back for these tests (no
// prompts, no fs/terminal use), so everything is a no-op / method-not-found.
func noopAgentHandler(_ context.Context, method string, _ json.RawMessage) (any, *acpsdk.RequestError) {
	return nil, acpsdk.NewMethodNotFound(method)
}

// spawnACPLeakAgent starts fir --mode acp in a clean directory with an
// isolated agent dir, and returns a raw connection, the spawned *exec.Cmd,
// and a cleanup func. Builtin extensions remain active so sessions spawn real
// python sidecars.
func spawnACPLeakAgent(t *testing.T, extraArgs ...string) (*acpsdk.Connection, *exec.Cmd, func()) {
	t.Helper()
	binary := firBinary(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}

	args := append([]string{
		"--mode", "acp",
		"--no-skills",
		"--no-themes",
		"--no-session",
		"--no-mcp",
	}, extraArgs...)
	cmd := exec.Command(binary, args...)

	// Run in a clean directory and isolated agent dir so discovery is
	// deterministic (only the binary's builtin extensions are active).
	cleanDir := t.TempDir()
	cmd.Dir = cleanDir
	agentDir := t.TempDir()
	cmd.Env = append(os.Environ(), "FIR_AGENT_DIR="+agentDir, "PWD="+cleanDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("stdin pipe:", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("stdout pipe:", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("start fir:", err)
	}

	conn := acpsdk.NewConnection(noopAgentHandler, stdin, stdout)
	cleanup := func() {
		stdin.Close()
		select {
		case <-conn.Done():
		case <-time.After(5 * time.Second):
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return conn, cmd, cleanup
}

// childPIDs returns the direct child pids of ppid via pgrep -P.
func childPIDs(ppid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(ppid)).Output()
	if err != nil {
		return nil // no children (pgrep exits 1) or pgrep missing
	}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// descendantPythonCount walks the process tree rooted at root and counts
// descendants whose command name contains "python".
func descendantPythonCount(root int) int {
	count := 0
	queue := childPIDs(root)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
		if err == nil && strings.Contains(strings.ToLower(string(out)), "python") {
			count++
		}
		queue = append(queue, childPIDs(pid)...)
	}
	return count
}

// waitForPythonCount polls until descendantPythonCount equals want or the
// deadline elapses; returns the last observed count.
func waitForPythonCount(root, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	last := descendantPythonCount(root)
	for time.Now().Before(deadline) {
		last = descendantPythonCount(root)
		if last == want {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

// waitForPythonCountAtLeast polls until descendantPythonCount >= want or the
// deadline elapses; returns the last observed count.
func waitForPythonCountAtLeast(root, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	last := descendantPythonCount(root)
	for time.Now().Before(deadline) {
		last = descendantPythonCount(root)
		if last >= want {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func rawInitialize(t *testing.T, conn *acpsdk.Connection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	params := acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}
	if _, err := acpsdk.SendRequest[json.RawMessage](conn, ctx, "initialize", params); err != nil {
		t.Fatalf("initialize: %v", err)
	}
}

// rawNewSession creates a session in cwd and returns its sessionId.
func rawNewSession(t *testing.T, conn *acpsdk.Connection, cwd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type resp struct {
		SessionId string `json:"sessionId"`
	}
	r, err := acpsdk.SendRequest[resp](conn, ctx, "session/new", map[string]any{"cwd": cwd})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if r.SessionId == "" {
		t.Fatal("empty sessionId")
	}
	return r.SessionId
}

// TestACP_E2E_SessionRelease_FreesPythonSidecars verifies that session/release
// tears down a session's python extension sidecars, returning the child
// python pid count to the (post-initialize) baseline.
func TestACP_E2E_SessionRelease_FreesPythonSidecars(t *testing.T) {
	const numSessions = 4

	conn, cmd, cleanup := spawnACPLeakAgent(t)
	defer cleanup()
	rootPID := cmd.Process.Pid

	rawInitialize(t, conn)
	// Baseline = python procs after init (global auth-provider extensions).
	// These persist across session teardown.
	baseline := waitForPythonCountStable(rootPID)

	cwd := t.TempDir()
	var sids []string
	for i := 0; i < numSessions; i++ {
		sids = append(sids, rawNewSession(t, conn, cwd))
	}

	// Each session spawns at least one python sidecar, so the count must
	// climb well above baseline.
	if got := waitForPythonCountAtLeast(rootPID, baseline+numSessions, 20*time.Second); got < baseline+numSessions {
		t.Fatalf("after creating %d sessions: python sidecars = %d, want >= %d (baseline %d)",
			numSessions, got, baseline+numSessions, baseline)
	}

	// Release every session — this must tear the per-session sidecars down.
	for _, sid := range sids {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := conn.SendRequestNoResult(ctx, "session/release", map[string]any{"sessionId": sid}); err != nil {
			cancel()
			t.Fatalf("session/release %s: %v", sid, err)
		}
		cancel()
	}

	if got := waitForPythonCount(rootPID, baseline, 20*time.Second); got != baseline {
		t.Fatalf("after releasing all sessions: python sidecars = %d, want baseline %d", got, baseline)
	}
}

// TestACP_E2E_ReleaseUnknownSession_TypedError verifies session/release on an
// unknown sessionId returns the typed SessionNotFound JSON-RPC error code.
func TestACP_E2E_ReleaseUnknownSession_TypedError(t *testing.T) {
	conn, _, cleanup := spawnACPLeakAgent(t)
	defer cleanup()
	rawInitialize(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := conn.SendRequestNoResult(ctx, "session/release", map[string]any{"sessionId": "ghost-session"})
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	re, ok := err.(*acpsdk.RequestError)
	if !ok {
		t.Fatalf("expected *acpsdk.RequestError, got %T (%v)", err, err)
	}
	// -32001 is acp.SessionNotFoundError (the shared contract code).
	if re.Code != -32001 {
		t.Errorf("error code = %d, want -32001", re.Code)
	}
}

// TestACP_E2E_IdleReaper_FreesPythonSidecars verifies the idle-session reaper
// tears down sessions left idle longer than --acp-session-idle-ttl, returning
// the child python pid count to baseline without any explicit release.
func TestACP_E2E_IdleReaper_FreesPythonSidecars(t *testing.T) {
	const numSessions = 3

	// 8s TTL → reaper ticks every ~1s (reaperIntervalFor). Long enough to
	// observe the sidecars start before they are reaped, short enough to keep
	// the test fast.
	conn, cmd, cleanup := spawnACPLeakAgent(t, "--acp-session-idle-ttl", "8s")
	defer cleanup()
	rootPID := cmd.Process.Pid

	rawInitialize(t, conn)
	baseline := waitForPythonCountStable(rootPID)

	cwd := t.TempDir()
	for i := 0; i < numSessions; i++ {
		rawNewSession(t, conn, cwd)
	}

	// Observe the sidecars come up (before the 8s idle window elapses).
	if got := waitForPythonCountAtLeast(rootPID, baseline+numSessions, 6*time.Second); got < baseline+numSessions {
		t.Fatalf("after creating %d sessions: python sidecars = %d, want >= %d (baseline %d)",
			numSessions, got, baseline+numSessions, baseline)
	}

	// Without touching the sessions again, the reaper should tear them all
	// down once they exceed the idle TTL.
	if got := waitForPythonCount(rootPID, baseline, 30*time.Second); got != baseline {
		t.Fatalf("idle reaper did not free sidecars: python sidecars = %d, want baseline %d", got, baseline)
	}
}

// rawPrompt sends a session/prompt for sid and returns any RPC error. The agent
// under test has no model configured, so the prompt resolves quickly with a
// non-fatal error (e.g. auth-required) rather than running real inference — the
// test only cares that it is NOT session-not-found.
func rawPrompt(t *testing.T, conn *acpsdk.Connection, sid, text string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params := map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": text}},
	}
	_, err := acpsdk.SendRequest[json.RawMessage](conn, ctx, "session/prompt", params)
	return err
}

// TestACP_E2E_IdleSession_ZeroSidecars_ThenWake is the end-to-end proof of the
// idle-zero design: an idle ACP session drops to ZERO python sidecars once it
// exceeds the idle TTL, and the next prompt on the SAME sessionId transparently
// re-hydrates it — no session-not-found, and the sidecars come back. This
// demonstrates "idle costs nothing, waking is seamless" against a real
// fir --mode acp subprocess.
func TestACP_E2E_IdleSession_ZeroSidecars_ThenWake(t *testing.T) {
	// 5s TTL → reaper ticks every ~1s.
	conn, cmd, cleanup := spawnACPLeakAgent(t, "--acp-session-idle-ttl", "5s")
	defer cleanup()
	rootPID := cmd.Process.Pid

	rawInitialize(t, conn)
	baseline := waitForPythonCountStable(rootPID)

	cwd := t.TempDir()
	sid := rawNewSession(t, conn, cwd)

	// The new session spawns at least one python sidecar.
	if got := waitForPythonCountAtLeast(rootPID, baseline+1, 10*time.Second); got < baseline+1 {
		t.Fatalf("after session/new: python sidecars = %d, want >= %d (baseline %d)",
			got, baseline+1, baseline)
	}

	// Leaving the session idle past the 5s TTL must drop the sidecars back to
	// baseline — an idle session holds ZERO of its own sidecars.
	if got := waitForPythonCount(rootPID, baseline, 30*time.Second); got != baseline {
		t.Fatalf("idle session did not reach zero sidecars: python sidecars = %d, want baseline %d",
			got, baseline)
	}

	// Wake the SAME conversation. The prompt must re-hydrate the session in
	// place: not session-not-found (-32001), and its sidecars must return.
	if err := rawPrompt(t, conn, sid, "are you awake?"); err != nil {
		if re, ok := err.(*acpsdk.RequestError); ok && re.Code == -32001 {
			t.Fatalf("prompt after idle returned session-not-found (-32001); expected lazy re-hydration")
		}
		// Any other error (e.g. auth-required because no model) is fine — the
		// session was still re-hydrated under the same id.
	}

	// Re-hydration must bring the session's sidecars back up.
	if got := waitForPythonCountAtLeast(rootPID, baseline+1, 20*time.Second); got < baseline+1 {
		t.Fatalf("waking the idle session did not restore its sidecars: python sidecars = %d, want >= %d (baseline %d)",
			got, baseline+1, baseline)
	}
}

// waitForPythonCountStable returns the python descendant count once it has
// stopped changing for a short window (so all eager init extensions have
// finished spawning before we record the baseline).
func waitForPythonCountStable(root int) int {
	deadline := time.Now().Add(15 * time.Second)
	prev := descendantPythonCount(root)
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		cur := descendantPythonCount(root)
		if cur != prev {
			prev = cur
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= 1*time.Second {
			return cur
		}
	}
	return prev
}
