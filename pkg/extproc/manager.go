package extproc

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/extproc/sdk"
)

// Manager owns all external process extension bridges for a session.
type Manager struct {
	logger  *slog.Logger
	trust   *TrustStore
	mu      sync.Mutex
	bridges []*managedBridge
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

// Start discovers extensions, spawns processes, performs handshakes, and
// starts bridge dispatch loops.
func (m *Manager) Start(ctx context.Context, projectDir string, cwd string, api BridgeAPI) error {
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
	// Trust check for project-local extensions.
	if cfg.Scope == "project" {
		hash, err := ComputeHash(cfg.Path)
		if err != nil {
			return err
		}
		if !m.trust.IsTrusted(projectDir, cfg.Name, hash) {
			m.logger.Warn("skipping untrusted project-local extension",
				"ext", cfg.Name, "path", cfg.Path)
			return nil
		}
	}

	proc := NewProcess(cfg, env, m.logger)
	if err := proc.Start(); err != nil {
		return err
	}

	caps, err := Handshake(proc, cwd)
	if err != nil {
		_ = proc.Stop(context.Background())
		return err
	}

	bridge := NewBridge(proc, caps)
	bridge.RegisterTools(api)

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

	// Send shutdown event to all extensions.
	for _, mb := range bridges {
		_ = mb.bridge.EmitEvent("session_shutdown", nil)
	}

	for _, mb := range bridges {
		mb.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = mb.proc.Stop(ctx)
		cancel()
	}
	return nil
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

// CallHook calls all bridges with the given hook and collects results.
func (m *Manager) CallHook(name string, data any, timeout time.Duration) ([]json.RawMessage, error) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	var results []json.RawMessage
	for _, mb := range bridges {
		raw, err := mb.bridge.CallHook(name, data, timeout)
		if err != nil {
			m.logger.Warn("hook call failed", "ext", mb.cfg.Name, "hook", name, "err", err)
			continue
		}
		if raw != nil {
			results = append(results, raw)
		}
	}
	return results, nil
}
