package extension

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension/sdk"
)

// authExtScript is a forkable extension that declares one imperative auth
// provider, mirroring the builtin auth extensions (anthropic etc.).
const authExtScript = `import fir_ext

@fir_ext.auth_provider(provider_id="lifecycle-auth", name="Lifecycle Auth")
def login(params, ctx):
    return {"access": "a", "refresh": "r", "expires": 0}

fir_ext.run(name="lifecycle-auth-ext")
`

const lifecycleAuthID = "lifecycle-auth"

// procGone reports whether pid no longer exists or is only a zombie.
func procGone(pid int) bool {
	if !pidAlive(pid) {
		return true
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	f := strings.Fields(string(data))
	return len(f) > 2 && f[2] == "Z"
}

// startForkedAuthExt starts the auth extension through the manager's fork
// path (as a builtin) and returns the manager and the running bridge.
func startForkedAuthExt(t *testing.T) (*Manager, *managedBridge) {
	t.Helper()
	fs, sdkDir := forkTestSetup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "lifecycle_auth.py")
	if err := os.WriteFile(path, []byte(authExtScript), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(slog.Default())
	mgr.fork = fs
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		_ = mgr.Stop()
		cancel()
	})
	cfg := ExtProcConfig{Name: "lifecycle-auth-ext", Path: path, Scope: "builtin"}
	if err := mgr.startOne(ctx, cfg, dir, sdk.SDKEnv(sdkDir), newMockAPI(), dir); err != nil {
		t.Fatalf("startOne: %v", err)
	}
	return mgr, currentBridge(mgr)
}

func currentBridge(m *Manager) *managedBridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bridges) == 0 {
		return nil
	}
	return m.bridges[0]
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A forked auth child killed from outside (e.g. a stray reap) must not leave
// the auth provider bound to a dead process: the manager respawns it and the
// fresh instance becomes the registered provider.
func TestChildLifecycle_KilledAuthChildRespawns(t *testing.T) {
	mgr, mb := startForkedAuthExt(t)
	old := ai.GetOAuthProvider(lifecycleAuthID)
	if old == nil {
		t.Fatal("auth provider not registered")
	}
	pid := mb.proc.Pid()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "respawned bridge", func() bool {
		cur := currentBridge(mgr)
		return cur != nil && cur != mb
	})
	cur := currentBridge(mgr)
	if got := ai.GetOAuthProvider(lifecycleAuthID); got == nil || got == old {
		t.Fatalf("provider after respawn = %v, want a fresh instance", got)
	}
	if npid := cur.proc.Pid(); npid == pid || !pidAlive(npid) {
		t.Fatalf("respawned pid %d (old %d) not live", npid, pid)
	}
	waitUntil(t, "dead child reaped", func() bool { return procGone(pid) })
}

// Past maxRespawns the dead extension is left down; its auth provider is
// unregistered and a sibling owner of the same ID (another session) becomes
// active again instead of auth breaking for everyone.
func TestChildLifecycle_KilledAuthChildUnregistersAfterMaxRespawns(t *testing.T) {
	sibling := &extAuthProvider{spec: AuthProviderSpec{ID: lifecycleAuthID, Name: "sibling"}}
	ai.RegisterOAuthProvider(sibling)
	t.Cleanup(func() { ai.UnregisterOAuthProviderInstance(sibling) })

	mgr, mb := startForkedAuthExt(t)
	if got := ai.GetOAuthProvider(lifecycleAuthID); got == nil || got == sibling {
		t.Fatalf("extension provider not active: %v", got)
	}
	mgr.mu.Lock()
	mgr.respawns = map[string]int{mb.cfg.Name: maxRespawns}
	mgr.lastDeath = map[string]time.Time{mb.cfg.Name: time.Now()}
	mgr.mu.Unlock()

	if err := syscall.Kill(mb.proc.Pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "provider fallback to sibling", func() bool {
		return ai.GetOAuthProvider(lifecycleAuthID) == sibling
	})
	if currentBridge(mgr) != nil {
		t.Fatal("dead extension was respawned past maxRespawns")
	}
}

// An exit already handled by Stop/Reload (bridge no longer tracked) is a no-op.
func TestChildLifecycle_HandleBridgeDiedIgnoresUntracked(t *testing.T) {
	mgr := NewManager(slog.Default())
	mgr.handleBridgeDied(context.Background(), &managedBridge{cfg: ExtProcConfig{Name: "x"}}, "", nil, nil, "")
	if mgr.respawns != nil {
		t.Fatal("untracked bridge should not count a respawn")
	}
}

// Stop on a forked child whose template is dead kills the child directly.
func TestChildLifecycle_StopForkedWithDeadTemplate(t *testing.T) {
	fs, _ := forkTestSetup(t)
	proc := NewProcess(ExtProcConfig{Name: "forktest", Path: writeTestExt(t), Scope: "builtin"}, nil, nil)
	if err := proc.StartForked(fs); err != nil {
		t.Fatalf("StartForked: %v", err)
	}
	pid := proc.Pid()
	_ = fs.Close()
	if err := proc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitUntil(t, "orphaned child killed", func() bool { return procGone(pid) })
}

// If even the direct kill fails, Stop reports the error instead of hiding it.
func TestChildLifecycle_StopForkedKillFails(t *testing.T) {
	fs, _ := forkTestSetup(t)
	_ = fs.Close()
	orig := killPid
	killPid = func(int, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { killPid = orig })
	proc := NewProcess(ExtProcConfig{Name: "ghost"}, nil, nil)
	proc.forkSrv = fs
	proc.forkPid = 424242
	err := proc.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kill") {
		t.Fatalf("Stop err = %v, want kill failure", err)
	}
}

// A respawn that fails to start leaves the extension down without panicking.
func TestChildLifecycle_RespawnStartFails(t *testing.T) {
	mgr, mb := startForkedAuthExt(t)
	// Make the next start fail: the script path no longer exists and the
	// fork template rejects it, then the exec fallback cannot run it.
	if err := os.Remove(mb.cfg.Path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(mb.proc.Pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "provider unregistered", func() bool {
		return ai.GetOAuthProvider(lifecycleAuthID) == nil
	})
	waitUntil(t, "respawn attempted", func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return mgr.respawns[mb.cfg.Name] == 1
	})
	if currentBridge(mgr) != nil {
		t.Fatal("bridge registered despite failed respawn")
	}
}
