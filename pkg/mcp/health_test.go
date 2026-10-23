package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/stretchr/testify/require"
)

func healthOf(t *testing.T, mgr *Manager, name string) ServerHealth {
	t.Helper()
	for _, h := range mgr.Health() {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("server %s missing from health", name)
	return ServerHealth{}
}

// healthRecorder collects health-change notifications on a channel.
func healthRecorder(mgr *Manager) <-chan []ServerHealth {
	ch := make(chan []ServerHealth, 64)
	mgr.SetOnHealthChanged(func(h []ServerHealth) { ch <- h })
	return ch
}

// waitHealth drains ch until a notification has srv in state, or fails.
func waitHealth(t *testing.T, ch <-chan []ServerHealth, state string) ServerHealth {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case hs := <-ch:
			for _, h := range hs {
				if h.Name == "srv" && h.State == state {
					return h
				}
			}
		case <-timeout:
			t.Fatalf("no health notification with state %s", state)
		}
	}
}

func TestHealth_UnauthenticatedThenOutOfBandLoginAndReload(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	authPath := filepath.Join(t.TempDir(), "auth.json")
	cfgs := map[string]ServerConfig{"srv": srv.config(nil)}

	mgr := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	mgr.SetAuth(auth.NewAuthStorage(authPath))
	ch := healthRecorder(mgr)
	mgr.SetSources(map[string]string{"srv": "/cfg/mcp.d/srv.json"})
	t.Cleanup(func() { _ = mgr.Close() })

	require.Error(t, connectAndWait(t, mgr))
	h := waitHealth(t, ch, StateNeedsAuth)
	require.Equal(t, "/mcp login srv", h.Fix)
	require.Equal(t, "/cfg/mcp.d/srv.json", h.Source)
	require.Zero(t, h.Tools)
	require.Equal(t, StateNeedsAuth, healthOf(t, mgr, "srv").State)
	require.Equal(t,
		"MCP server srv: configured, NOT AUTHENTICATED, tools unavailable -> fir command: /mcp login srv (if logged in elsewhere: /mcp reload srv)",
		RenderHealthSection(mgr.Health()))

	cli := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	cli.SetAuth(auth.NewAuthStorage(authPath))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, cli.LoginServer(ctx, "srv", browserCallbacks(t, nil)))
	require.NoError(t, cli.Close())

	_, err := mgr.Reload(ctx, cfgs)
	require.NoError(t, err)
	h = waitHealth(t, ch, StateOK)
	require.Positive(t, h.Tools)
	require.Empty(t, h.Fix)
	require.Empty(t, RenderHealthSection(mgr.Health()))
}

func TestHealth_MidSessionRevokeReportsExpiredThenLoginRecovers(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))
	ch := healthRecorder(mgr)
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	waitHealth(t, ch, StateOK)

	as.revokeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err)
	h := waitHealth(t, ch, StateTokenExpired)
	require.Equal(t, "/mcp login srv", h.Fix)
	require.Contains(t, RenderHealthSection(mgr.Health()), "TOKEN EXPIRED")

	require.NoError(t, mgr.LoginServer(ctx, "srv", browserCallbacks(t, nil)))
	waitHealth(t, ch, StateOK)
	require.Empty(t, RenderHealthSection(mgr.Health()))
}

func TestHealth_DisabledServerIsReportedNotDialled(t *testing.T) {
	srv := newFakeMCPServer(t, nil, mcpOptions{open: true})
	cfg := srv.config(nil)
	cfg.Disabled = true
	mgr, _ := newTestManager(t, cfg)
	mgr.SetSources(map[string]string{"srv": "/p/.fir/mcp.json"})
	require.NoError(t, connectAndWait(t, mgr))

	h := healthOf(t, mgr, "srv")
	require.Equal(t, StateDisabled, h.State)
	require.NotContains(t, mgr.ToolsByServer(), "srv", "a disabled server advertises no saved tools")
	require.Equal(t, "disabled", mgr.Details()[0].Status)
	require.Contains(t, h.Fix, "/p/.fir/mcp.json")
	unauth, authd := srv.counters()
	require.Zero(t, unauth+authd, "a disabled server is never dialled")

	// Enabling it via reload connects it.
	cfg.Disabled = false
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := mgr.Reload(ctx, map[string]ServerConfig{"srv": cfg})
	require.NoError(t, err)
	require.Equal(t, StateOK, healthOf(t, mgr, "srv").State)
}

func TestRenderHealthSection(t *testing.T) {
	require.Empty(t, RenderHealthSection(nil))
	require.Empty(t, RenderHealthSection([]ServerHealth{{Name: "a", State: StateOK}, {Name: "b", State: StateConnecting}}))

	long := strings.Repeat("x", 300)
	out := RenderHealthSection([]ServerHealth{
		{Name: "a", State: StateOK},
		{Name: "b", State: StateError, Error: "boom\n  bad", Fix: "/mcp reload b"},
		{Name: "c", State: StateDisabled, Fix: fixFor(ServerHealth{Name: "c", State: StateDisabled})},
		{Name: "d", State: StateError, Error: long, Fix: "/mcp reload d"},
	})
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 3)
	require.Equal(t, "MCP server b: configured, ERROR, tools unavailable -> fir command: /mcp reload b [boom bad]", lines[0])
	require.Contains(t, lines[1], "DISABLED")
	require.Contains(t, lines[1], "its MCP config")
	require.Less(t, len(lines[2]), 300)
}

func TestConfigSources(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", cfgDir)
	proj := t.TempDir()
	write := func(p, body string) {
		require.NoError(t, osMkdirWrite(p, body))
	}
	write(filepath.Join(cfgDir, "mcp.json"), `{"mcpServers":{"a":{"command":"x"},"b":{"command":"x"}}}`)
	write(filepath.Join(cfgDir, "mcp.d", "slack.json"), `{"mcpServers":{"slack":{"transport":"streamable","url":"https://example.com/mcp"}}}`)
	write(filepath.Join(proj, ".fir", "mcp.json"), `{"mcpServers":{"b":{"command":"y"}}}`)

	src := ConfigSources(proj)
	require.Equal(t, filepath.Join(cfgDir, "mcp.json"), src["a"])
	require.Equal(t, filepath.Join(cfgDir, "mcp.d", "slack.json"), src["slack"])
	require.Equal(t, filepath.Join(proj, ".fir", "mcp.json"), src["b"])
}

func osMkdirWrite(p, body string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o600)
}
