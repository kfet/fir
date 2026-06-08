package extension

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// testExtScript is a minimal extension exercising the full SDK run loop over
// whatever stdin/stdout it is given (here: the forked child's unix socket).
const testExtScript = `import fir_ext

@fir_ext.tool(
    name="ping",
    description="ping",
    parameters={"type": "object", "properties": {}},
)
def ping(params, ctx):
    return {"content": [{"text": "pong"}], "is_error": False}

fir_ext.run(name="forktest")
`

// forkTestSetup extracts the SDK and starts a fork template, skipping the test
// if python3 / the SDK is unavailable.
func forkTestSetup(t *testing.T) (*ForkServer, string) {
	t.Helper()
	sdkDir, err := sdk.EnsureExtracted()
	if err != nil {
		t.Skipf("SDK unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sdkDir, "python", "forkserver.py")); err != nil {
		t.Skipf("forkserver template missing: %v", err)
	}
	fs, err := StartForkServer(sdkDir, sdk.SDKEnv(sdkDir), nil)
	if err != nil {
		t.Skipf("fork template unavailable (python3?): %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs, sdkDir
}

// writeTestExt writes the minimal test extension to a temp .py file.
func writeTestExt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "forktest.py")
	if err := os.WriteFile(path, []byte(testExtScript), 0o644); err != nil {
		t.Fatalf("write test ext: %v", err)
	}
	return path
}

// pidAlive reports whether pid is still a live process (signal 0 probe).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func TestForkServer_SpawnHandshakeToolReap(t *testing.T) {
	fs, _ := forkTestSetup(t)
	path := writeTestExt(t)

	cfg := ExtProcConfig{Name: "forktest", Path: path, Scope: "builtin"}
	proc := NewProcess(cfg, nil, nil)
	if err := proc.StartForked(fs); err != nil {
		t.Fatalf("StartForked: %v", err)
	}
	pid := proc.Pid()
	if pid <= 0 {
		t.Fatalf("expected forked pid, got %d", pid)
	}
	if !pidAlive(pid) {
		t.Fatalf("forked child %d not alive", pid)
	}

	// The forked child must speak JSON-RPC over its private socket channel.
	caps, err := Handshake(proc, "/tmp", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if caps.Name != "forktest" {
		t.Fatalf("handshake name = %q, want forktest", caps.Name)
	}
	if len(caps.Tools) != 1 || caps.Tools[0].Name != "ping" {
		t.Fatalf("unexpected tools: %+v", caps.Tools)
	}

	// Invoke the tool and read the response on the same codec.
	codec := proc.GetCodec()
	if err := codec.WriteRequest(2, "tool_call", map[string]any{
		"tool_call_id": "tc1",
		"name":         "ping",
		"params":       map[string]any{},
	}); err != nil {
		t.Fatalf("write tool_call: %v", err)
	}
	msg, err := codec.ReadMessage()
	if err != nil {
		t.Fatalf("read tool response: %v", err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("tool error: %v", resp.Error)
	}

	// Teardown reaps the child via the template (its parent).
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The child should be gone shortly after stop.
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("forked child %d still alive after Stop", pid)
	}
}

// TestForkServer_MultipleChildrenIsolated verifies that two children forked
// from the same template register independent handler state (each runs its own
// extension module despite sharing the COW heap) and both work concurrently.
func TestForkServer_MultipleChildrenIsolated(t *testing.T) {
	fs, _ := forkTestSetup(t)
	path := writeTestExt(t)
	cfg := ExtProcConfig{Name: "forktest", Path: path, Scope: "builtin"}

	const n = 4
	procs := make([]*Process, n)
	for i := range procs {
		p := NewProcess(cfg, nil, nil)
		if err := p.StartForked(fs); err != nil {
			t.Fatalf("StartForked[%d]: %v", i, err)
		}
		if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err != nil {
			t.Fatalf("handshake[%d]: %v", i, err)
		}
		procs[i] = p
	}
	// Distinct pids.
	seen := map[int]bool{}
	for _, p := range procs {
		pid := p.Pid()
		if seen[pid] {
			t.Fatalf("duplicate pid %d", pid)
		}
		seen[pid] = true
	}
	for _, p := range procs {
		_ = p.Stop(context.Background())
	}
}

func TestForkServer_CloseReapsTemplate(t *testing.T) {
	fs, _ := forkTestSetup(t)
	pid := fs.Pid()
	if pid <= 0 {
		t.Fatalf("expected template pid")
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("template %d still alive after Close", pid)
	}
}

func TestForkEligible(t *testing.T) {
	cases := []struct {
		cfg  ExtProcConfig
		want bool
	}{
		{ExtProcConfig{Scope: "builtin", Path: "/x/mood.py"}, true},
		{ExtProcConfig{Scope: "builtin", Path: "/x/hello.js"}, false},
		{ExtProcConfig{Scope: "builtin", Path: "/x/tool.sh"}, false},
		{ExtProcConfig{Scope: "project", Path: "/x/p.py"}, false},
		{ExtProcConfig{Scope: "global", Path: "/x/g.py"}, false},
	}
	for _, c := range cases {
		if got := forkEligible(c.cfg); got != c.want {
			t.Errorf("forkEligible(%+v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}

func TestEnvSliceToMap(t *testing.T) {
	m := envSliceToMap([]string{"A=1", "B=x=y", "noeq", "C="})
	if m["A"] != "1" || m["B"] != "x=y" || m["C"] != "" {
		t.Fatalf("unexpected map: %+v", m)
	}
	if _, ok := m["noeq"]; ok {
		t.Fatalf("entry without '=' should be skipped")
	}
	if envSliceToMap(nil) != nil {
		t.Fatalf("nil env should map to nil")
	}
}
