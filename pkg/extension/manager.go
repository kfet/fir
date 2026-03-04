package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// ConfirmFunc asks the user whether to trust an extension.
// It receives the extension name and file path, and returns true to trust.
type ConfirmFunc func(name, path string) bool

// Manager owns all external process extension bridges for a session.
type Manager struct {
	logger    *slog.Logger
	trust     *TrustStore
	mu        sync.Mutex
	bridges   []*managedBridge
	ConfirmFn ConfirmFunc

	// AllowedNames is an optional allowlist of extension names. When non-empty,
	// only extensions whose Name appears in this list are started.
	AllowedNames []string

	// Optional UI callbacks, applied to each bridge when it starts.
	notifyFn    NotifyFunc
	setStatusFn SetStatusFunc

	// Saved from Start() for Reload().
	projectDir string
	cwd        string
	api        BridgeAPI
}

type managedBridge struct {
	cfg    ExtProcConfig
	proc   *Process
	bridge *Bridge
	cancel context.CancelFunc
}

// NewManager creates a Manager with the given logger.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		logger: logger,
		trust:  NewTrustStore(),
	}
}

// SetTrustStore overrides the default TrustStore (useful for testing).
func (m *Manager) SetTrustStore(ts *TrustStore) {
	m.trust = ts
}

// SetNotifyFn sets the notification callback applied to all new bridges.
// Call before Start() or Reload() to take effect.
func (m *Manager) SetNotifyFn(fn NotifyFunc) {
	m.mu.Lock()
	m.notifyFn = fn
	m.mu.Unlock()
}

// SetSetStatusFn sets the status callback applied to all new bridges.
// Call before Start() or Reload() to take effect.
func (m *Manager) SetSetStatusFn(fn SetStatusFunc) {
	m.mu.Lock()
	m.setStatusFn = fn
	m.mu.Unlock()
}

// SetAllowedNames updates the optional extension allowlist.
// Pass an empty slice to allow all discovered extensions.
func (m *Manager) SetAllowedNames(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AllowedNames = append([]string(nil), names...)
}

// Start discovers extensions, spawns processes, performs handshakes, and
// starts bridge dispatch loops.
func (m *Manager) Start(ctx context.Context, projectDir string, cwd string, api BridgeAPI) error {
	m.mu.Lock()
	m.projectDir = projectDir
	m.cwd = cwd
	m.api = api
	m.mu.Unlock()

	configs, err := Discover(projectDir)
	if err != nil {
		return err
	}

	// Extract SDKs and build env.
	var sdkEnv []string
	sdkPath, err := sdk.EnsureExtracted()
	if err != nil {
		m.logger.Warn("failed to extract SDKs", "err", err)
	} else {
		sdkEnv = sdk.SDKEnv(sdkPath)
	}

	for _, cfg := range configs {
		if err := m.startOne(ctx, cfg, cwd, sdkEnv, api, projectDir); err != nil {
			m.logger.Warn("failed to start extension",
				"ext", cfg.Name, "err", err)
		}
	}
	return nil
}

func (m *Manager) startOne(ctx context.Context, cfg ExtProcConfig, cwd string, env []string, api BridgeAPI, projectDir string) error {
	// Allowlist check: skip extensions not in AllowedNames when the list is set.
	m.mu.Lock()
	allowed := m.AllowedNames
	m.mu.Unlock()
	if len(allowed) > 0 && !containsString(allowed, cfg.Name) {
		m.logger.Debug("skipping extension (not in allowlist)", "ext", cfg.Name)
		return nil
	}

	// Trust check for project-local extensions (builtins are always trusted).
	if cfg.Scope == "project" {
		hash, err := ComputeHash(cfg.Path)
		if err != nil {
			return err
		}
		if !m.trust.IsTrusted(projectDir, cfg.Name, hash) {
			if m.ConfirmFn != nil && m.ConfirmFn(cfg.Name, cfg.Path) {
				if err := m.trust.RecordTrust(projectDir, cfg.Name, hash); err != nil {
					return fmt.Errorf("recording trust: %w", err)
				}
			} else {
				m.logger.Warn("skipping untrusted project-local extension",
					"ext", cfg.Name, "path", cfg.Path)
				return nil
			}
		}
	}

	proc := NewProcess(cfg, env, m.logger)
	if err := proc.Start(); err != nil {
		return err
	}

	caps, err := Handshake(proc, cwd, 0)
	if err != nil {
		_ = proc.Stop(context.Background())
		return err
	}

	bridge := NewBridge(proc, caps)
	bridge.RegisterTools(api)

	// Wire optional UI callbacks.
	m.mu.Lock()
	notifyFn := m.notifyFn
	setStatusFn := m.setStatusFn
	m.mu.Unlock()
	if notifyFn != nil {
		bridge.NotifyFn = notifyFn
	}
	if setStatusFn != nil {
		bridge.SetStatusFn = setStatusFn
	}

	bCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := bridge.Run(bCtx, api); err != nil && bCtx.Err() == nil {
			m.logger.Warn("bridge exited", "ext", cfg.Name, "err", err)
		}
	}()

	m.mu.Lock()
	m.bridges = append(m.bridges, &managedBridge{
		cfg:    cfg,
		proc:   proc,
		bridge: bridge,
		cancel: cancel,
	})
	m.mu.Unlock()

	m.logger.Info("started extension", "ext", caps.Name,
		"tools", len(caps.Tools), "events", len(caps.Events))
	return nil
}

// Stop shuts down all bridges and processes.
// Sends session_shutdown event before stopping.
func (m *Manager) Stop() error {
	m.mu.Lock()
	bridges := m.bridges
	m.bridges = nil
	m.mu.Unlock()

	// Send shutdown event to all extensions and give them a moment
	// to handle it (e.g. restore tmux window names) before killing.
	for _, mb := range bridges {
		_ = mb.bridge.EmitEvent("session_shutdown", nil)
	}

	// Brief grace period so extensions can process the shutdown event.
	time.Sleep(250 * time.Millisecond)

	for _, mb := range bridges {
		mb.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = mb.proc.Stop(ctx)
		cancel()
	}
	return nil
}

// Reload stops all running extensions and re-discovers/starts them.
// The provided context controls the lifetime of the new bridges.
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	projectDir := m.projectDir
	cwd := m.cwd
	api := m.api
	m.mu.Unlock()

	if projectDir == "" || api == nil {
		return fmt.Errorf("manager was not started; cannot reload")
	}

	// Stop existing extensions (emits session_shutdown).
	_ = m.Stop()

	// Re-discover and start.
	return m.Start(ctx, projectDir, cwd, api)
}

// EmitEvent fans out a notification to all bridges.
func (m *Manager) EmitEvent(name string, data any) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		if err := mb.bridge.EmitEvent(name, data); err != nil {
			m.logger.Warn("emit event failed", "ext", mb.cfg.Name, "event", name, "err", err)
		}
	}
}

// CallHook calls all bridges with the given hook and collects results concurrently.
func (m *Manager) CallHook(name string, data any, timeout time.Duration) ([]json.RawMessage, error) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	type indexedResult struct {
		idx int
		raw json.RawMessage
	}

	var (
		wg      sync.WaitGroup
		resMu   sync.Mutex
		results []json.RawMessage
	)

	for i, mb := range bridges {
		wg.Add(1)
		go func(idx int, mb *managedBridge) {
			defer wg.Done()
			raw, err := mb.bridge.CallHook(name, data, timeout)
			if err != nil {
				m.logger.Warn("hook call failed", "ext", mb.cfg.Name, "hook", name, "err", err)
				return
			}
			if raw != nil {
				resMu.Lock()
				results = append(results, raw)
				resMu.Unlock()
			}
		}(i, mb)
	}
	wg.Wait()

	return results, nil
}

// containsString reports whether s is in slice.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// ExtCommand holds a slash command declared by an extension.
type ExtCommand struct {
	ExtName string
	Spec    CommandSpec
}

// GetCommands returns all slash commands declared by running extensions.
func (m *Manager) GetCommands() []ExtCommand {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	var cmds []ExtCommand
	for _, mb := range bridges {
		for _, spec := range mb.bridge.caps.Commands {
			cmds = append(cmds, ExtCommand{ExtName: mb.cfg.Name, Spec: spec})
		}
	}
	return cmds
}

// CommandResult is the result of dispatching a slash command to an extension.
type CommandResult struct {
	// Message is optional text shown to the user in the TUI.
	Message string `json:"message"`
}

// DispatchCommand sends a hook/command call to the extension that owns name.
// args are the whitespace-split arguments after the command name (may be empty).
// Returns ErrCommandNotFound if no extension registered that command name.
func (m *Manager) DispatchCommand(name string, args []string, timeout time.Duration) (CommandResult, error) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		for _, spec := range mb.bridge.caps.Commands {
			if spec.Name == name {
				params := map[string]any{
					"name": name,
					"args": args,
				}
				raw, err := mb.bridge.CallHook("hook/command", params, timeout)
				if err != nil {
					return CommandResult{}, fmt.Errorf("extension %s command %q: %w", mb.cfg.Name, name, err)
				}
				var result CommandResult
				if raw != nil {
					// Best-effort parse; ignore errors.
					_ = json.Unmarshal(raw, &result)
				}
				return result, nil
			}
		}
	}
	return CommandResult{}, fmt.Errorf("extension: no command %q registered", name)
}
