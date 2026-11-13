package extension

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// idleExtScript records how it was started: its pid, the session data it was
// given on session_start, and the last session_end reason it saw (persisted as
// session data so a resumed child can report it).
const idleExtScript = `import os
import fir_ext

_state = {"restored": "", "named": ""}

@fir_ext.on("session_start")
def on_start(params, ctx):
    _state["restored"] = ((params or {}).get("session_data") or {}).get("count", "")

@fir_ext.on("session_named")
def on_named(params, ctx):
    _state["named"] = (params or {}).get("name", "")

@fir_ext.on("session_end")
def on_end(params, ctx):
    ctx.set_session_data("end_reason", (params or {}).get("reason", ""))

@fir_ext.tool(name="idle_ping", description="ping", parameters={"type": "object", "properties": {}})
def ping(params, ctx):
    n = int(ctx.get_session_data("count") or "0") + 1
    ctx.set_session_data("count", str(n))
    return {"content": [{"text": "%d|%d|%s|%s" % (os.getpid(), n, _state["restored"], _state["named"])}], "is_error": False}

@fir_ext.command(name="idleping", description="ping")
def cmd(args, ctx):
    return {"message": "pong %d" % os.getpid()}

fir_ext.run(name="idle-ext")
`

const keepAliveExtScript = `import fir_ext

@fir_ext.tool(name="keep_ping", description="ping", parameters={"type": "object", "properties": {}})
def ping(params, ctx):
    return {"content": [{"text": "ok"}], "is_error": False}

fir_ext.run(name="keep-ext")
`

type idleHarness struct {
	mgr *Manager
	api *mockBridgeAPI
}

// startIdleHarness starts idle-ext (stoppable) and keep-ext (keep_alive) as
// forked builtins under one manager, wired like Manager.Start would.
func startIdleHarness(t *testing.T) *idleHarness {
	t.Helper()
	fs, sdkDir := forkTestSetup(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	idlePath := write("idle_ext.py", idleExtScript)
	keepPath := write("keep_ext.py", keepAliveExtScript)

	api := newMockAPI()
	api.sessionName = "idle-session"
	mgr := NewManager(slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	env := sdk.SDKEnv(sdkDir)
	mgr.mu.Lock()
	mgr.fork = fs
	mgr.startCtx, mgr.cwd, mgr.projectDir, mgr.api, mgr.sdkEnv = ctx, dir, dir, api, env
	mgr.mu.Unlock()
	t.Cleanup(func() {
		_ = mgr.Stop()
		cancel()
	})
	for _, cfg := range []ExtProcConfig{
		{Name: "idle-ext", Path: idlePath, Scope: "builtin"},
		{Name: "keep-ext", Path: keepPath, Scope: "builtin", KeepAlive: true},
	} {
		if err := mgr.startOne(ctx, cfg, dir, env, api, dir); err != nil {
			t.Fatalf("startOne %s: %v", cfg.Name, err)
		}
	}
	return &idleHarness{mgr: mgr, api: api}
}

func (h *idleHarness) bridge(name string) *managedBridge {
	h.mgr.mu.Lock()
	defer h.mgr.mu.Unlock()
	for _, mb := range h.mgr.bridges {
		if mb.cfg.Name == name {
			return mb
		}
	}
	return nil
}

// callTool runs a registered tool through the session-facing definition, the
// way the agent would.
func (h *idleHarness) callTool(t *testing.T, name string) string {
	t.Helper()
	h.api.mu.Lock()
	var def *ToolDefinition
	for i := range h.api.toolsRegistered {
		if h.api.toolsRegistered[i].Name == name {
			d := h.api.toolsRegistered[i]
			def = &d
		}
	}
	h.api.mu.Unlock()
	if def == nil {
		t.Fatalf("tool %s not registered", name)
	}
	res, err := def.Execute(ToolContext{Context: context.Background(), ToolCallID: "c1", Params: map[string]any{}})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("tool %s: res=%+v err=%v", name, res, err)
	}
	return res.Content[0].Text
}

func splitPing(t *testing.T, s string) (pid, count int, restored, named string) {
	t.Helper()
	var parts [4]string
	i := 0
	start := 0
	for j := 0; j < len(s) && i < 3; j++ {
		if s[j] == '|' {
			parts[i] = s[start:j]
			i++
			start = j + 1
		}
	}
	parts[3] = s[start:]
	pid, _ = strconv.Atoi(parts[0])
	count, _ = strconv.Atoi(parts[1])
	return pid, count, parts[2], parts[3]
}

// TestIdleStop_E2E: idle -> forked child gone (pid) -> prompt -> child back,
// state replayed, tools and commands work; keep_alive child untouched.
func TestIdleStop_E2E(t *testing.T) {
	h := startIdleHarness(t)
	pid1, n, _, _ := splitPing(t, h.callTool(t, "idle_ping"))
	if n != 1 || pid1 <= 0 {
		t.Fatalf("first ping: pid=%d count=%d", pid1, n)
	}
	keepPid := h.bridge("keep-ext").proc.Pid()

	stopped := h.mgr.IdleStop()
	if len(stopped) != 1 || stopped[0] != "idle-ext" {
		t.Fatalf("IdleStop stopped %v, want [idle-ext]", stopped)
	}
	waitUntil(t, "idle child gone", func() bool { return procGone(pid1) })
	if h.bridge("idle-ext") != nil {
		t.Fatal("idle-ext still tracked after idle stop")
	}
	if procGone(keepPid) {
		t.Fatal("keep_alive child was stopped")
	}
	if h.api.toolNameSet()["idle_ping"] {
		t.Fatal("idle_ping tool still registered while stopped")
	}
	if got := h.mgr.IdleStopped(); len(got) != 1 || got[0] != "idle-ext" {
		t.Fatalf("IdleStopped=%v", got)
	}
	// Commands of idle-stopped extensions stay listed.
	found := false
	for _, c := range h.mgr.GetCommands() {
		found = found || c.Spec.Name == "idleping"
	}
	if !found {
		t.Fatal("idleping command not listed while idle-stopped")
	}

	stats := h.mgr.Resume()
	if len(stats.PerExt) != 1 || stats.PerExt["idle-ext"] <= 0 || stats.Total < stats.PerExt["idle-ext"] || len(stats.Failed) != 0 {
		t.Fatalf("resume stats: %+v", stats)
	}
	t.Logf("resume latency: idle-ext=%v total=%v", stats.PerExt["idle-ext"], stats.Total)

	pid2, n, restored, named := splitPing(t, h.callTool(t, "idle_ping"))
	if pid2 == pid1 || pid2 <= 0 {
		t.Fatalf("expected a new child, pid before=%d after=%d", pid1, pid2)
	}
	if restored != "1" || n != 2 {
		t.Fatalf("session data not replayed: restored=%q count=%d", restored, n)
	}
	if named != "idle-session" {
		t.Fatalf("session_named not replayed: %q", named)
	}
	if got := h.bridge("idle-ext").bridge.GetAllSessionData()["end_reason"]; got != IdleStopReason {
		t.Fatalf("session_end reason = %q, want %q", got, IdleStopReason)
	}
	if h.bridge("keep-ext").proc.Pid() != keepPid {
		t.Fatal("keep_alive child restarted")
	}
	if len(h.mgr.Resume().PerExt) != 0 {
		t.Fatal("second Resume restarted something")
	}

	// A slash command resumes too.
	h.mgr.IdleStop()
	res, err := h.mgr.DispatchCommand("idleping", nil, 10*time.Second)
	if err != nil || res.Message == "" || res.Message == "pong "+strconv.Itoa(pid2) {
		t.Fatalf("command after idle: %+v err=%v", res, err)
	}
}

// TestIdleStop_NoRespawnBudget: idle stops never count as crashes.
func TestIdleStop_NoRespawnBudget(t *testing.T) {
	h := startIdleHarness(t)
	for i := 0; i < maxRespawns+2; i++ {
		if got := h.mgr.IdleStop(); len(got) != 1 {
			t.Fatalf("cycle %d: IdleStop=%v", i, got)
		}
		if st := h.mgr.Resume(); len(st.PerExt) != 1 {
			t.Fatalf("cycle %d: Resume=%+v", i, st)
		}
	}
	h.mgr.mu.Lock()
	n := h.mgr.respawns["idle-ext"]
	h.mgr.mu.Unlock()
	if n != 0 {
		t.Fatalf("respawn budget used by idle stops: %d", n)
	}
	h.callTool(t, "idle_ping")
}

// TestIdleStop_Timer: the timer fires only while not busy, and Stop cancels it.
func TestIdleStop_Timer(t *testing.T) {
	h := startIdleHarness(t)
	h.mgr.SetBusy(true)
	h.mgr.SetIdleStop(time.Millisecond)
	if h.mgr.IdleStop() != nil {
		t.Fatal("IdleStop ran while busy")
	}
	h.mgr.SetBusy(false)
	waitUntil(t, "timer idle stop", func() bool { return len(h.mgr.IdleStopped()) == 1 })
	h.mgr.SetIdleStop(0)
	h.mgr.Resume()
	if len(h.mgr.IdleStopped()) != 0 || h.bridge("idle-ext") == nil {
		t.Fatal("resume after timer stop failed")
	}
	_ = h.mgr.Stop()
	if h.mgr.IdleStop() != nil || len(h.mgr.Resume().PerExt) != 0 {
		t.Fatal("idle stop/resume acted after Stop")
	}
}

func TestIdleStoppable(t *testing.T) {
	forked := &Process{forkSrv: &ForkServer{}}
	plain := &Process{}
	caps := func(c InitResult) *Bridge { return &Bridge{caps: &c} }
	cases := []struct {
		name string
		mb   *managedBridge
		want bool
	}{
		{"forked", &managedBridge{proc: forked, bridge: caps(InitResult{})}, true},
		{"exec", &managedBridge{proc: plain, bridge: caps(InitResult{})}, false},
		{"keepalive", &managedBridge{cfg: ExtProcConfig{KeepAlive: true}, proc: forked, bridge: caps(InitResult{})}, false},
		{"fm auth", &managedBridge{cfg: ExtProcConfig{AuthProviders: []string{"x"}}, proc: forked, bridge: caps(InitResult{})}, false},
		{"caps auth", &managedBridge{proc: forked, bridge: caps(InitResult{AuthProviders: []AuthProviderSpec{{}}})}, false},
		{"provider", &managedBridge{proc: forked, bridge: caps(InitResult{Providers: []ProviderSpec{{}}})}, false},
		{"api", &managedBridge{proc: forked, bridge: caps(InitResult{Apis: []ApiSpec{{}}})}, false},
	}
	for _, c := range cases {
		if got := idleStoppable(c.mb); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestIdleStop_RealBuiltins idles and resumes the real builtin extension set
// and logs resume latency per child and in total (the numbers in the release
// notes come from here).
func TestIdleStop_RealBuiltins(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns every builtin extension")
	}
	if _, err := sdk.EnsureExtracted(); err != nil {
		t.Skipf("SDK unavailable: %v", err)
	}
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	api := newMockAPI()
	mgr := NewManager(slog.Default())
	mgr.ActiveMode = "interactive"
	if err := mgr.Start(context.Background(), dir, dir, api); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	mgr.mu.Lock()
	forked := mgr.fork != nil
	mgr.mu.Unlock()
	if !forked {
		t.Skip("fork template unavailable")
	}
	stopped := mgr.IdleStop()
	if len(stopped) == 0 {
		t.Fatal("no builtin was idle-stopped")
	}
	for _, keep := range []string{"schedule", "observe", "tmuxspinner"} {
		for _, s := range stopped {
			if s == keep {
				t.Errorf("keep_alive builtin %s was stopped", keep)
			}
		}
	}
	st := mgr.Resume()
	if len(st.Failed) != 0 || len(st.PerExt) != len(stopped) {
		t.Fatalf("resume: %+v (stopped %v)", st, stopped)
	}
	var slowest time.Duration
	for n, d := range st.PerExt {
		t.Logf("resume %-20s %v", n, d)
		if d > slowest {
			slowest = d
		}
	}
	t.Logf("resumed %d children: total=%v slowest=%v", len(st.PerExt), st.Total, slowest)
}

// TestSetup_BeforeTurnResumesIdleStopped: the session hook Setup installs
// brings idle-stopped builtins back before a turn and marks the session busy.
func TestSetup_BeforeTurnResumesIdleStopped(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	s := newSetupTestSession(t, projectDir)
	res, err := Setup(s, SetupOptions{
		ProjectDir:     projectDir,
		Cwd:            projectDir,
		EnabledNames:   []string{"notify"},
		Mode:           "interactive",
		TrustStorePath: filepath.Join(t.TempDir(), "trusted.json"),
	})
	if err != nil || res == nil {
		t.Fatalf("Setup: %v", err)
	}
	defer res.EmitSessionShutdown()
	mgr := res.Manager
	if got := mgr.IdleStop(); len(got) != 1 || got[0] != "notify" {
		t.Fatalf("IdleStop=%v, want [notify]", got)
	}
	s.Hooks().BeforeTurn()
	if len(mgr.IdleStopped()) != 0 || len(mgr.RunningExtensions()) != 1 {
		t.Fatalf("BeforeTurn did not resume: idle=%v running=%v", mgr.IdleStopped(), mgr.RunningExtensions())
	}
	if mgr.IdleStop() != nil {
		t.Fatal("idle stop ran while a turn is starting")
	}
}
