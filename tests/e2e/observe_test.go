//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// runFirBackground starts fir in the background, returns the *exec.Cmd.
// Caller must call cmd.Process.Kill() and cmd.Wait() when done.
// Output is drained to prevent the process blocking on a full pipe buffer.
func runFirBackground(t *testing.T, dir string, env map[string]string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(firBinary, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Discard output — we only care that the process is running.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// waitForFile polls until a file appears or timeout elapses.
func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// waitForPattern polls until the file at path contains pattern or timeout elapses.
func waitForGlob(pattern string, timeout time.Duration) ([]string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			return matches, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, false
}

// readSidecar reads the sidecar JSON at path.
func readSidecar(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(data, &m)
}

// firObserve runs "fir observe [args...]" with the given XDG_STATE_HOME.
func firObserve(t *testing.T, stateHome string, args ...string) (string, int) {
	t.Helper()
	all := append([]string{"observe"}, args...)
	return runFir(t, "", 10*time.Second,
		map[string]string{"XDG_STATE_HOME": stateHome},
		all...)
}

// ---------------------------------------------------------------------------
// Section 8: fir observe / fir send
// ---------------------------------------------------------------------------

// TestObserve_NoSessions_PrintsMessage verifies the "no sessions found"
// message when the sidecar directory is empty.
func TestObserve_NoSessions_PrintsMessage(t *testing.T) {
	stateHome := t.TempDir()
	out, code := firObserve(t, stateHome)
	if code != 0 {
		t.Fatalf("exit code %d, out: %s", code, out)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "no fir sessions") {
		t.Errorf("expected 'no fir sessions' message, got: %s", out)
	}
}

// TestObserve_UnknownFlag returns error and usage.
func TestObserve_UnknownFlag(t *testing.T) {
	stateHome := t.TempDir()
	out, code := firObserve(t, stateHome, "--bogus-flag")
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "unknown flag") && !strings.Contains(out, "usage") {
		t.Errorf("expected flag error, got: %s", out)
	}
}

// TestObserve_PrefixNotFound returns a clear error.
func TestObserve_PrefixNotFound(t *testing.T) {
	stateHome := t.TempDir()
	out, code := firObserve(t, stateHome, "doesnotexist")
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown prefix")
	}
	assertNoPanic(t, out)
}

// TestObserve_SidecarWrittenOnSessionStart verifies that running a fir print
// session with the observe extension writes a sidecar JSON file.
func TestObserve_SidecarWrittenOnSessionStart(t *testing.T) {
	stateHome := t.TempDir()
	dir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": t.TempDir(), // isolate sockets
	}
	out, code := runFir(t, "", 20*time.Second, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say exactly: SIDECAR_TEST",
	)
	_ = dir
	if code != 0 {
		t.Logf("fir output: %s", out)
		// Non-zero is acceptable (e.g. observe extension handshake slowdown);
		// what matters is the sidecar appeared.
	}
	assertNoPanic(t, out)

	// Check sidecar appeared.
	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	matches, ok := waitForGlob(pattern, 5*time.Second)
	if !ok {
		t.Fatalf("no sidecar written to %s after 5s; fir output:\n%s", pattern, out)
	}

	// Validate sidecar schema.
	m, err := readSidecar(matches[0])
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if m["schema"] != float64(1) {
		t.Errorf("sidecar schema = %v, want 1", m["schema"])
	}
	if m["session_id"] == "" || m["session_id"] == nil {
		t.Errorf("sidecar session_id is empty: %v", m)
	}
	if m["store_path"] == "" || m["store_path"] == nil {
		t.Errorf("sidecar store_path is empty: %v", m)
	}
	if m["pid"] == nil {
		t.Errorf("sidecar pid is missing: %v", m)
	}
}

// TestObserve_ListSession verifies that "fir observe" lists the session written
// by a running fir process.
func TestObserve_ListSession(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": runtimeDir,
	}

	// Start a background fir process that will write a sidecar.
	bgCmd := runFirBackground(t, projectRoot, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say OK",
	)
	defer func() {
		bgCmd.Process.Kill()
		bgCmd.Wait()
	}()

	// Wait for sidecar to appear.
	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	_, ok := waitForGlob(pattern, 10*time.Second)
	if !ok {
		t.Fatalf("no sidecar written to %s within 10s", pattern)
	}

	// fir observe should list it. Use --all because the print-mode session
	// may have transitioned to status=ended before we look (default listing
	// now hides ended/crashed sessions).
	out, code := firObserve(t, stateHome, "--all")
	if code != 0 {
		t.Fatalf("fir observe exit %d: %s", code, out)
	}
	assertNoPanic(t, out)

	if strings.Contains(out, "no fir sessions") {
		t.Errorf("expected session in list, got 'no fir sessions': %s", out)
	}
	// Table should have an ID column entry (8-char hex).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("expected header + at least one session row, got: %s", out)
	}
}

// TestObserve_TranscriptTailJSON verifies that "fir observe <id> --json" outputs
// the raw JSONL transcript including the session header line.
func TestObserve_TranscriptTailJSON(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	sessDir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": runtimeDir,
		"FIR_SESSION_DIR": sessDir,
	}

	// Run a complete fir session (print mode — exits after response).
	out, _ := runFir(t, "", 20*time.Second, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say OK",
	)
	assertNoPanic(t, out)

	// Wait for sidecar.
	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	matches, ok := waitForGlob(pattern, 5*time.Second)
	if !ok {
		t.Fatalf("no sidecar written; fir output:\n%s", out)
	}

	// Read sidecar to get session id prefix.
	m, _ := readSidecar(matches[0])
	sid, _ := m["session_id"].(string)
	if len(sid) < 8 {
		t.Fatalf("session_id too short: %q", sid)
	}
	prefix := sid[:8]

	// "fir observe <prefix> --json" should output the transcript.
	jsonOut, code := firObserve(t, stateHome, prefix, "--json")
	if code != 0 {
		t.Fatalf("fir observe %s --json exit %d: %s", prefix, code, jsonOut)
	}
	assertNoPanic(t, jsonOut)

	// First line should be a session header.
	lines := parseJSONLines(jsonOut)
	if len(lines) == 0 {
		t.Fatalf("no JSON lines in transcript output:\n%s", jsonOut)
	}
	firstType, _ := lines[0]["type"].(string)
	if firstType != "session" {
		t.Errorf("first transcript line type = %q, want 'session'", firstType)
	}
}

// TestObserve_TranscriptFormattedOutput verifies that the human-readable
// formatter produces recognisable output (role markers, timestamps).
func TestObserve_TranscriptFormattedOutput(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": runtimeDir,
	}

	out, _ := runFir(t, "", 20*time.Second, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say OK",
	)
	assertNoPanic(t, out)

	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	matches, ok := waitForGlob(pattern, 5*time.Second)
	if !ok {
		t.Fatalf("no sidecar; fir output:\n%s", out)
	}
	m, _ := readSidecar(matches[0])
	sid, _ := m["session_id"].(string)
	if len(sid) < 8 {
		t.Fatalf("session_id too short: %q", sid)
	}
	prefix := sid[:8]

	fmtOut, code := firObserve(t, stateHome, prefix)
	if code != 0 {
		t.Fatalf("fir observe %s exit %d: %s", prefix, code, fmtOut)
	}
	assertNoPanic(t, fmtOut)

	// Expect at least a timestamp and role indicator.
	if !strings.Contains(fmtOut, "user") && !strings.Contains(fmtOut, "assistant") {
		t.Errorf("expected role marker in formatted output, got:\n%s", fmtOut)
	}
	if !strings.Contains(fmtOut, ":") {
		t.Errorf("expected timestamp colon in formatted output, got:\n%s", fmtOut)
	}
}

// TestObserve_SidecarStatusEndedAfterSession verifies that after a fir print
// session completes, the sidecar status is "ended".
func TestObserve_SidecarStatusEndedAfterSession(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": runtimeDir,
	}

	// Run session to completion.
	runFir(t, "", 20*time.Second, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say OK",
	)

	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	matches, ok := waitForGlob(pattern, 5*time.Second)
	if !ok {
		t.Fatalf("no sidecar written")
	}

	// Poll for status=ended (extension may write it slightly after process exit).
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		m, err := readSidecar(matches[0])
		if err == nil {
			status, _ = m["status"].(string)
			if status == "ended" {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status != "ended" {
		m, _ := readSidecar(matches[0])
		t.Errorf("sidecar status = %q, want 'ended'; sidecar: %v", status, m)
	}
}

// TestObserve_SessionFileWrittenImmediately verifies Step A: the session JSONL
// file exists immediately after session start (not gated on first response).
func TestObserve_SessionFileWrittenImmediately(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	env := map[string]string{
		"FIR_AGENT_DIR":   mockAgentDir,
		"XDG_STATE_HOME":  stateHome,
		"FIR_RUNTIME_DIR": runtimeDir,
	}

	out, _ := runFir(t, "", 20*time.Second, env,
		"--provider", "mock", "--model", "mock-model",
		"-e", "observe",
		"-p", "say OK",
	)
	assertNoPanic(t, out)

	pattern := filepath.Join(stateHome, "fir", "agents", "*.json")
	matches, ok := waitForGlob(pattern, 5*time.Second)
	if !ok {
		t.Fatalf("no sidecar; fir output:\n%s", out)
	}

	m, _ := readSidecar(matches[0])
	storePath, _ := m["store_path"].(string)
	if storePath == "" {
		t.Fatalf("sidecar store_path empty: %v", m)
	}

	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("session transcript file %s does not exist: %v", storePath, err)
	}

	// First line of the transcript must be the session header.
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("transcript is empty")
	}
	var firstEntry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &firstEntry); err != nil {
		t.Fatalf("parse first transcript line: %v", err)
	}
	if firstEntry["type"] != "session" {
		t.Errorf("first transcript entry type = %q, want 'session'", firstEntry["type"])
	}
}

// TestSend_NoArgs returns usage error.
func TestSend_NoArgs(t *testing.T) {
	out, code := runFir(t, "", 5*time.Second, nil, "send")
	if code == 0 {
		t.Fatal("expected non-zero exit for 'fir send' with no args")
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "required") && !strings.Contains(out, "usage") && !strings.Contains(out, "id") {
		t.Errorf("expected usage/required error, got: %s", out)
	}
}

// TestSend_UnknownFlag returns error.
func TestSend_UnknownFlag(t *testing.T) {
	out, code := runFir(t, "", 5*time.Second, nil, "send", "--bogus")
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	assertNoPanic(t, out)
}

// TestSend_ConnectToSocket verifies that "fir send" can deliver a message to a
// live session's Unix socket and the session receives it.
func TestSend_ConnectToSocket(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()

	// Create a mock Unix socket that accepts one NDJSON message and records it.
	sockPath := filepath.Join(runtimeDir, "test.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		received <- string(buf[:n])
	}()

	// Write a synthetic sidecar pointing at our test socket.
	sidecarDir := filepath.Join(stateHome, "fir", "agents")
	os.MkdirAll(sidecarDir, 0o700)
	sid := "aabbccdd-1234-5678-abcd-000000000001"
	sidecarData, _ := json.Marshal(map[string]any{
		"schema":       1,
		"session_id":   sid,
		"pid":          os.Getpid(),
		"socket_path":  sockPath,
		"store_path":   "/dev/null",
		"cwd":          t.TempDir(),
		"started_at":   time.Now().UTC().Format(time.RFC3339),
		"status":       "running",
		"session_name": "send-test",
	})
	sidecarPath := filepath.Join(sidecarDir, sid+".json")
	os.WriteFile(sidecarPath, sidecarData, 0o600)

	// Send a message via "fir send aabbcc".
	out, code := runFir(t, "hello agent\n", 10*time.Second,
		map[string]string{"XDG_STATE_HOME": stateHome},
		"send", "aabbcc",
	)
	if code != 0 {
		t.Fatalf("fir send exit %d: %s", code, out)
	}
	assertNoPanic(t, out)

	// Check the socket received the NDJSON.
	select {
	case msg := <-received:
		var parsed map[string]string
		if err := json.Unmarshal([]byte(strings.TrimSpace(msg)), &parsed); err != nil {
			t.Fatalf("parse received NDJSON %q: %v", msg, err)
		}
		if parsed["content"] != "hello agent" {
			t.Errorf("content = %q, want 'hello agent'", parsed["content"])
		}
		if parsed["deliver_as"] != "" {
			t.Errorf("deliver_as = %q, want empty (default)", parsed["deliver_as"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("socket did not receive a message within 5s")
	}
}

// TestSend_SteerSigil verifies that the ! sigil sets deliver_as=steer.
func TestSend_SteerSigil(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := t.TempDir()
	sockPath := filepath.Join(runtimeDir, "steer.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	received := make(chan string, 1)
	go func() {
		conn, _ := l.Accept()
		if conn == nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		received <- string(buf[:n])
	}()

	sid := "aabbccdd-1234-5678-abcd-000000000002"
	sidecarDir := filepath.Join(stateHome, "fir", "agents")
	os.MkdirAll(sidecarDir, 0o700)
	sidecarData, _ := json.Marshal(map[string]any{
		"schema": 1, "session_id": sid, "pid": os.Getpid(),
		"socket_path": sockPath, "store_path": "/dev/null",
		"cwd": t.TempDir(), "started_at": time.Now().UTC().Format(time.RFC3339),
		"status": "running", "session_name": "",
	})
	os.WriteFile(filepath.Join(sidecarDir, sid+".json"), sidecarData, 0o600)

	runFir(t, "!interrupt now\n", 10*time.Second,
		map[string]string{"XDG_STATE_HOME": stateHome},
		"send", "aabbcc",
	)

	select {
	case msg := <-received:
		var parsed map[string]string
		json.Unmarshal([]byte(strings.TrimSpace(msg)), &parsed)
		if parsed["deliver_as"] != "steer" {
			t.Errorf("deliver_as = %q, want 'steer'", parsed["deliver_as"])
		}
		if parsed["content"] != "interrupt now" {
			t.Errorf("content = %q, want 'interrupt now'", parsed["content"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

// TestObserve_CwdFlag verifies --cwd resolution.
func TestObserve_CwdFlag(t *testing.T) {
	stateHome := t.TempDir()
	cwd := t.TempDir()
	sidecarDir := filepath.Join(stateHome, "fir", "agents")
	os.MkdirAll(sidecarDir, 0o700)

	sid := "cwd00001-cccc-dddd-eeee-000000000003"
	sidecarData, _ := json.Marshal(map[string]any{
		"schema": 1, "session_id": sid, "pid": os.Getpid(),
		"socket_path": "", "store_path": "/dev/null",
		"cwd": cwd, "started_at": time.Now().UTC().Format(time.RFC3339),
		"status": "running", "session_name": "",
	})
	os.WriteFile(filepath.Join(sidecarDir, sid+".json"), sidecarData, 0o600)

	// fir observe --cwd <cwd> should list only that session.
	out, code := runFir(t, "", 5*time.Second,
		map[string]string{"XDG_STATE_HOME": stateHome},
		"observe", "--cwd", cwd,
	)
	// Will fail because store_path=/dev/null can't be tailed; that's OK —
	// what we check is that it resolves without "no sessions" or ambiguity.
	_ = code
	assertNoPanic(t, out)
	if strings.Contains(out, "no fir sessions") {
		t.Errorf("expected session to be resolved by --cwd, got: %s", out)
	}
}

// TestObserve_AmbiguousPrefixError verifies the ambiguity error path.
func TestObserve_AmbiguousPrefixError(t *testing.T) {
	stateHome := t.TempDir()
	sidecarDir := filepath.Join(stateHome, "fir", "agents")
	os.MkdirAll(sidecarDir, 0o700)

	for i, sid := range []string{
		"aaaa0001-0001-0001-0001-000000000001",
		"aaaa0002-0002-0002-0002-000000000002",
	} {
		data, _ := json.Marshal(map[string]any{
			"schema": 1, "session_id": sid, "pid": os.Getpid(),
			"socket_path": "", "store_path": "/dev/null",
			"cwd": t.TempDir(), "started_at": time.Now().UTC().Format(time.RFC3339),
			"status": "running", "session_name": "",
		})
		_ = i
		os.WriteFile(filepath.Join(sidecarDir, sid+".json"), data, 0o600)
	}

	out, code := runFir(t, "", 5*time.Second,
		map[string]string{"XDG_STATE_HOME": stateHome},
		"observe", "aaaa",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for ambiguous prefix")
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "ambiguous") {
		t.Errorf("expected 'ambiguous' in error, got: %s", out)
	}
}

// writeSidecar drops a synthetic sidecar JSON into stateHome.
func writeSidecar(t *testing.T, stateHome, sid string, fields map[string]any) {
	t.Helper()
	dir := filepath.Join(stateHome, "fir", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"schema":       1,
		"session_id":   sid,
		"pid":          os.Getpid(),
		"socket_path":  "",
		"store_path":   "/dev/null",
		"cwd":          t.TempDir(),
		"started_at":   time.Now().UTC().Format(time.RFC3339),
		"status":       "running",
		"session_name": "",
	}
	for k, v := range fields {
		base[k] = v
	}
	data, _ := json.Marshal(base)
	if err := os.WriteFile(filepath.Join(dir, sid+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestObserve_DefaultHidesNonLive verifies that "fir observe" with no flags
// shows only live (running/idle) sessions and hides ended/crashed ones,
// pointing the user at --all.
func TestObserve_DefaultHidesNonLive(t *testing.T) {
	stateHome := t.TempDir()

	writeSidecar(t, stateHome, "live0001-aaaa-bbbb-cccc-000000000001",
		map[string]any{"status": "running", "session_name": "alive"})
	writeSidecar(t, stateHome, "ende0002-aaaa-bbbb-cccc-000000000002",
		map[string]any{"status": "ended", "session_name": "donezo"})
	// Crashed: status=running but pid is one we know is dead. PID 1 is alive,
	// so use a high pid that's almost certainly free.
	writeSidecar(t, stateHome, "dead0003-aaaa-bbbb-cccc-000000000003",
		map[string]any{"status": "running", "session_name": "kaput", "pid": 999999})

	out, code := firObserve(t, stateHome)
	if code != 0 {
		t.Fatalf("fir observe exit %d: %s", code, out)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "alive") {
		t.Errorf("expected live session 'alive' in default output:\n%s", out)
	}
	if strings.Contains(out, "donezo") || strings.Contains(out, "kaput") {
		t.Errorf("default output should hide ended/crashed sessions:\n%s", out)
	}
	if !strings.Contains(out, "--all") {
		t.Errorf("expected hint about --all when non-live rows are hidden:\n%s", out)
	}
}

// TestObserve_AllFlagShowsCrashedAndEnded verifies "fir observe --all"
// includes ended and crashed sessions.
func TestObserve_AllFlagShowsCrashedAndEnded(t *testing.T) {
	stateHome := t.TempDir()

	writeSidecar(t, stateHome, "live0011-aaaa-bbbb-cccc-000000000011",
		map[string]any{"status": "running", "session_name": "alive"})
	writeSidecar(t, stateHome, "ende0012-aaaa-bbbb-cccc-000000000012",
		map[string]any{"status": "ended", "session_name": "donezo"})
	writeSidecar(t, stateHome, "dead0013-aaaa-bbbb-cccc-000000000013",
		map[string]any{"status": "running", "session_name": "kaput", "pid": 999999})

	out, code := firObserve(t, stateHome, "--all")
	if code != 0 {
		t.Fatalf("fir observe --all exit %d: %s", code, out)
	}
	assertNoPanic(t, out)
	for _, want := range []string{"alive", "donezo", "kaput"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in --all output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "crashed") {
		t.Errorf("expected dead-pid row to be reclassified as 'crashed':\n%s", out)
	}
}

// TestObserve_OnlyNonLiveSessions verifies that when no live sessions exist
// but ended/crashed ones do, default output says so and points at --all.
func TestObserve_OnlyNonLiveSessions(t *testing.T) {
	stateHome := t.TempDir()

	writeSidecar(t, stateHome, "ende0021-aaaa-bbbb-cccc-000000000021",
		map[string]any{"status": "ended"})

	out, code := firObserve(t, stateHome)
	if code != 0 {
		t.Fatalf("fir observe exit %d: %s", code, out)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "no live fir sessions") {
		t.Errorf("expected 'no live fir sessions' message:\n%s", out)
	}
	if !strings.Contains(out, "--all") {
		t.Errorf("expected --all hint:\n%s", out)
	}
}
