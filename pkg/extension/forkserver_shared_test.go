package extension

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// sharedForkTestSDK extracts the SDK, skipping the test when it (or python3)
// is unavailable, and registers cleanup that tears down all shared templates
// so no warm python3 leaks across tests.
func sharedForkTestSDK(t *testing.T) string {
	t.Helper()
	sdkDir, err := sdk.EnsureExtracted()
	if err != nil {
		t.Skipf("SDK unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sdkDir, "python", "forkserver.py")); err != nil {
		t.Skipf("forkserver template missing: %v", err)
	}
	t.Cleanup(CloseSharedForkServers)
	return sdkDir
}

// TestSharedForkServer_Singleton verifies the process-lifetime keying: the
// same SDK dir yields the same template instance, including under concurrent
// first-use (two sessions starting at once).
func TestSharedForkServer_Singleton(t *testing.T) {
	sdkDir := sharedForkTestSDK(t)
	env := sdk.SDKEnv(sdkDir)

	const n = 4
	got := make([]*ForkServer, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fs, err := SharedForkServer(sdkDir, env, nil)
			if err != nil {
				t.Errorf("SharedForkServer[%d]: %v", i, err)
				return
			}
			got[i] = fs
		}(i)
	}
	wg.Wait()
	if got[0] == nil {
		t.Skip("fork template unavailable (python3?)")
	}
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("expected one shared instance, got distinct instances at [0] and [%d]", i)
		}
	}
}

// TestSharedForkServer_ReplacesClosed verifies a dead/closed cached template
// is replaced with a fresh usable one instead of being handed back.
func TestSharedForkServer_ReplacesClosed(t *testing.T) {
	sdkDir := sharedForkTestSDK(t)
	env := sdk.SDKEnv(sdkDir)

	fs1, err := SharedForkServer(sdkDir, env, nil)
	if err != nil {
		t.Skipf("fork template unavailable (python3?): %v", err)
	}
	if !fs1.usable() {
		t.Fatalf("fresh template not usable")
	}
	_ = fs1.Close()
	if fs1.usable() {
		t.Fatalf("closed template still reports usable")
	}

	fs2, err := SharedForkServer(sdkDir, env, nil)
	if err != nil {
		t.Fatalf("SharedForkServer after close: %v", err)
	}
	if fs2 == fs1 {
		t.Fatalf("expected closed template to be replaced, got same instance")
	}
	if !fs2.usable() {
		t.Fatalf("replacement template not usable")
	}
}

// TestSharedForkServer_PerSessionChildReaping is the cross-session invariant:
// stopping one session's forked children must not touch another session's
// children or the shared template itself.
func TestSharedForkServer_PerSessionChildReaping(t *testing.T) {
	sdkDir := sharedForkTestSDK(t)
	env := sdk.SDKEnv(sdkDir)

	fs, err := SharedForkServer(sdkDir, env, nil)
	if err != nil {
		t.Skipf("fork template unavailable (python3?): %v", err)
	}
	path := writeTestExt(t)
	cfg := ExtProcConfig{Name: "forktest", Path: path, Scope: "builtin"}

	// "Session A" and "session B" each fork a child off the shared template.
	procA := NewProcess(cfg, nil, nil)
	if err := procA.StartForked(fs); err != nil {
		t.Fatalf("StartForked A: %v", err)
	}
	procB := NewProcess(cfg, nil, nil)
	if err := procB.StartForked(fs); err != nil {
		t.Fatalf("StartForked B: %v", err)
	}
	pidA, pidB := procA.Pid(), procB.Pid()
	if !pidAlive(pidA) || !pidAlive(pidB) {
		t.Fatalf("children not alive: A=%d B=%d", pidA, pidB)
	}

	// Session A ends: its child is stopped and reaped via the template.
	if err := procA.Stop(context.Background()); err != nil {
		t.Fatalf("stop A: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(pidA) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pidA) {
		t.Fatalf("session A child %d still alive after Stop", pidA)
	}

	// Session B's child and the shared template are untouched.
	if !pidAlive(pidB) {
		t.Fatalf("session B child %d died when session A stopped", pidB)
	}
	if !fs.usable() {
		t.Fatalf("shared template no longer usable after session A stop")
	}
	if !pidAlive(fs.Pid()) {
		t.Fatalf("shared template process %d gone after session A stop", fs.Pid())
	}

	// And the template still serves new spawns afterwards.
	procC := NewProcess(cfg, nil, nil)
	if err := procC.StartForked(fs); err != nil {
		t.Fatalf("StartForked after session A stop: %v", err)
	}
	_ = procC.Stop(context.Background())
	_ = procB.Stop(context.Background())
}

// TestSharedForkServer_RecoversFromCrashedTemplate verifies that a template
// that dies without Close (e.g. crashes) is replaced on the next acquisition
// instead of being handed back forever. The crashed template lingers as a
// zombie (fir never Wait()ed it), so a signal-0 probe alone would keep
// reporting it alive; the control-channel error path must mark it closed.
func TestSharedForkServer_RecoversFromCrashedTemplate(t *testing.T) {
	sdkDir := sharedForkTestSDK(t)
	env := sdk.SDKEnv(sdkDir)

	fs1, err := SharedForkServer(sdkDir, env, nil)
	if err != nil {
		t.Skipf("fork template unavailable (python3?): %v", err)
	}
	pid := fs1.Pid()

	// Crash the template out from under us (SIGKILL: no cleanup, becomes a
	// zombie child of this process until reaped).
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill template: %v", err)
	}

	// A spawn against the crashed template must fail and mark it unusable.
	// No settling wait is needed: the control read blocks until the dying
	// process closes its end of the pipe, so EOF is deterministic.
	path := writeTestExt(t)
	if _, _, err := fs1.Spawn("forktest", path, nil); err == nil {
		t.Fatalf("expected spawn against crashed template to fail")
	}

	// The next acquisition must hand back a fresh, working template.
	fs2, err := SharedForkServer(sdkDir, env, nil)
	if err != nil {
		t.Fatalf("SharedForkServer after crash: %v", err)
	}
	if fs2 == fs1 {
		t.Fatalf("expected crashed template to be replaced, got same instance")
	}
	proc := NewProcess(ExtProcConfig{Name: "forktest", Path: path, Scope: "builtin"}, nil, nil)
	if err := proc.StartForked(fs2); err != nil {
		t.Fatalf("StartForked on replacement: %v", err)
	}
	_ = proc.Stop(context.Background())
}
