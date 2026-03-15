package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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
	pending   []*pendingExtension // extensions deferred for lazy startup
	ConfirmFn ConfirmFunc

	// AllowedNames is an optional allowlist of extension names. When non-empty,
	// only extensions whose Name appears in this list are started.
	AllowedNames []string

	// ActiveMode is the currently running fir mode (interactive, text, json, rpc, acp).
	// Extensions with mode constraints that do not include this mode are skipped.
	ActiveMode string

	// Optional UI callbacks, applied to each bridge when it starts.
	notifyFn    NotifyFunc
	setStatusFn SetStatusFunc

	// OfferFixFn is called when a frontmatter mismatch is detected after
	// handshake. It receives the mismatch details and returns true if the
	// user wants the frontmatter auto-fixed. When nil, only a warning is logged.
	OfferFixFn func(mm FrontmatterMismatch) bool

	// Saved from Start() for Reload().
	projectDir          string
	cwd                 string
	api                 BridgeAPI
	sdkEnv              []string // cached SDK env vars
	extraExtensionDirs  []string // extra dirs from installed packages
	extraExtensionFiles []string // extra script files from installed packages
}

type managedBridge struct {
	cfg    ExtProcConfig
	proc   *Process
	bridge *Bridge
	cancel context.CancelFunc
}

// pendingExtension is an extension that declared its events/commands in
// frontmatter and will be started lazily on first matching event or command.
type pendingExtension struct {
	cfg    ExtProcConfig
	events map[string]bool // event names this extension subscribes to
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

// SetExtraExtensionDirs sets additional directories to scan for extension
// scripts. These are merged at lower priority (shadowed by project/global).
// Call before Start.
func (m *Manager) SetExtraExtensionDirs(dirs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extraExtensionDirs = append([]string(nil), dirs...)
}

// SetExtraExtensionFiles sets individual extension script paths contributed
// by installed packages. These are merged at lower priority.
// Call before Start.
func (m *Manager) SetExtraExtensionFiles(files []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extraExtensionFiles = append([]string(nil), files...)
}

// Start discovers extensions, spawns processes, performs handshakes, and
// starts bridge dispatch loops.
//
// Extensions that declare their subscribed events in frontmatter are deferred
// for lazy startup — they are only spawned when the first matching event fires.
// Extensions without frontmatter events are started eagerly (in parallel).
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

	// Merge in package-contributed extension dirs at lower priority.
	// The byName map from Discover ensures project/global already win.
	m.mu.Lock()
	extraDirs := append([]string(nil), m.extraExtensionDirs...)
	extraFiles := append([]string(nil), m.extraExtensionFiles...)
	m.mu.Unlock()
	if len(extraDirs) > 0 || len(extraFiles) > 0 {
		// Build a name set from existing configs so we don't shadow them.
		existing := make(map[string]bool, len(configs))
		for _, c := range configs {
			existing[c.Name] = true
		}
		if len(extraDirs) > 0 {
			extraConfigs, extraErr := DiscoverExtra(extraDirs)
			if extraErr == nil {
				for _, c := range extraConfigs {
					if !existing[c.Name] {
						configs = append(configs, c)
						existing[c.Name] = true
					}
				}
			}
		}
		if len(extraFiles) > 0 {
			for _, c := range ConfigsFromFiles(extraFiles) {
				if !existing[c.Name] {
					configs = append(configs, c)
					existing[c.Name] = true
				}
			}
		}
	}

	// Extract SDKs and build env.
	var sdkEnv []string
	sdkPath, err := sdk.EnsureExtracted()
	if err != nil {
		m.logger.Warn("failed to extract SDKs", "err", err)
	} else {
		sdkEnv = sdk.SDKEnv(sdkPath)
	}

	m.mu.Lock()
	m.sdkEnv = sdkEnv
	m.mu.Unlock()

	// Partition into eager (no frontmatter events) and lazy (has frontmatter events).
	var eager []ExtProcConfig
	for _, cfg := range configs {
		if m.shouldSkip(cfg) {
			continue
		}
		if len(cfg.Events) > 0 {
			// Defer: register as pending, start on first matching event.
			eventSet := make(map[string]bool, len(cfg.Events))
			for _, e := range cfg.Events {
				eventSet[e] = true
			}
			m.mu.Lock()
			m.pending = append(m.pending, &pendingExtension{cfg: cfg, events: eventSet})
			m.mu.Unlock()
			m.logger.Debug("deferred extension for lazy start", "ext", cfg.Name, "events", cfg.Events)
		} else {
			eager = append(eager, cfg)
		}
	}

	// Start eager extensions concurrently — handshakes involve spawning
	// Python interpreters and are I/O-bound, so parallelism helps a lot.
	var wg sync.WaitGroup
	for _, cfg := range eager {
		wg.Add(1)
		go func(cfg ExtProcConfig) {
			defer wg.Done()
			if err := m.startOne(ctx, cfg, cwd, sdkEnv, api, projectDir); err != nil {
				m.logger.Warn("failed to start extension",
					"ext", cfg.Name, "err", err)
			}
		}(cfg)
	}
	wg.Wait()
	return nil
}

// shouldSkip returns true if the extension should be skipped based on
// allowlist, demo, and mode filtering. Extracted from startOne so that
// Start can partition before spawning goroutines.
func (m *Manager) shouldSkip(cfg ExtProcConfig) bool {
	m.mu.Lock()
	allowed := m.AllowedNames
	m.mu.Unlock()
	if len(allowed) > 0 && !containsString(allowed, cfg.Name) {
		return true
	}
	if cfg.Demo && !containsString(allowed, cfg.Name) {
		return true
	}
	if !extensionSupportsMode(cfg.Modes, m.ActiveMode) {
		return true
	}
	return false
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
	// Demo extensions are skipped unless explicitly in the allowlist.
	if cfg.Demo && !containsString(allowed, cfg.Name) {
		m.logger.Debug("skipping demo extension (not explicitly allowed)", "ext", cfg.Name)
		return nil
	}
	if !extensionSupportsMode(cfg.Modes, m.ActiveMode) {
		m.logger.Debug("skipping extension (mode mismatch)", "ext", cfg.Name, "activeMode", m.ActiveMode, "modes", cfg.Modes)
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

	// Validate frontmatter against actual handshake capabilities.
	if mm := CheckFrontmatter(cfg, caps); !mm.Empty() {
		m.logger.Warn(FormatFrontmatterWarning(mm))
		m.mu.Lock()
		offerFix := m.OfferFixFn
		m.mu.Unlock()
		if offerFix != nil && offerFix(mm) {
			if err := FixFrontmatter(cfg.Path, caps); err != nil {
				m.logger.Warn("failed to fix frontmatter", "ext", cfg.Name, "err", err)
			} else {
				m.logger.Info("fixed frontmatter", "ext", cfg.Name)
			}
		}
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

	// Wrap the shared api with a per-bridge scoped wrapper so that
	// set_session_data / get_session_data are routed to this bridge's own
	// storage rather than the shared SessionBridge.
	scopedAPI := &bridgeScopedAPI{BridgeAPI: api, b: bridge}

	bCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := bridge.Run(bCtx, scopedAPI); err != nil && bCtx.Err() == nil {
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
	m.pending = nil // discard any un-triggered lazy extensions
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

	// Remove previously registered extension tools to avoid duplicates.
	if ur, ok := api.(interface{ UnregisterExtensionTools() }); ok {
		ur.UnregisterExtensionTools()
	}

	// Re-discover and start.
	return m.Start(ctx, projectDir, cwd, api)
}

// startPendingForEvent starts any pending (lazy) extensions that subscribe to
// the given event or hook name. It removes them from the pending list and
// starts them in parallel, blocking until all are ready.
func (m *Manager) startPendingForEvent(name string) {
	m.mu.Lock()
	var toStart []ExtProcConfig
	var remaining []*pendingExtension
	for _, pe := range m.pending {
		if pe.events[name] {
			toStart = append(toStart, pe.cfg)
		} else {
			remaining = append(remaining, pe)
		}
	}
	if len(toStart) > 0 {
		m.pending = remaining
	}
	cwd := m.cwd
	sdkEnv := m.sdkEnv
	api := m.api
	projectDir := m.projectDir
	m.mu.Unlock()

	if len(toStart) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, cfg := range toStart {
		wg.Add(1)
		go func(cfg ExtProcConfig) {
			defer wg.Done()
			if err := m.startOne(context.Background(), cfg, cwd, sdkEnv, api, projectDir); err != nil {
				m.logger.Warn("failed to lazy-start extension",
					"ext", cfg.Name, "trigger", name, "err", err)
			} else {
				m.logger.Info("lazy-started extension", "ext", cfg.Name, "trigger", name)
			}
		}(cfg)
	}
	wg.Wait()
}

// EmitEvent fans out a notification to all bridges.
// If any pending (lazy) extensions subscribe to this event, they are started
// first so they receive it.
func (m *Manager) EmitEvent(name string, data any) {
	m.startPendingForEvent(name)

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
// If any pending (lazy) extensions subscribe to this hook, they are started
// first so their hook handlers are active.
func (m *Manager) CallHook(name string, data any, timeout time.Duration) ([]json.RawMessage, error) {
	m.startPendingForEvent(name)

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

// EnabledExtensionNames returns the enabled extension names for this manager.
//
// If AllowedNames is configured, that list is returned (sorted, deduplicated)
// so callers can surface the user's configured enablement.
// Otherwise, names of currently running extensions are returned.
func (m *Manager) EnabledExtensionNames() []string {
	m.mu.Lock()
	allowed := append([]string(nil), m.AllowedNames...)
	bridges := append([]*managedBridge(nil), m.bridges...)
	pending := append([]*pendingExtension(nil), m.pending...)
	m.mu.Unlock()

	names := make(map[string]struct{})
	if len(allowed) > 0 {
		for _, name := range allowed {
			if name == "" {
				continue
			}
			names[name] = struct{}{}
		}
	} else {
		for _, mb := range bridges {
			if mb.cfg.Name == "" {
				continue
			}
			names[mb.cfg.Name] = struct{}{}
		}
		for _, pe := range pending {
			if pe.cfg.Name != "" {
				names[pe.cfg.Name] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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
	pending := append([]*pendingExtension(nil), m.pending...)
	m.mu.Unlock()

	var cmds []ExtCommand
	for _, mb := range bridges {
		for _, spec := range mb.bridge.caps.Commands {
			cmds = append(cmds, ExtCommand{ExtName: mb.cfg.Name, Spec: spec})
		}
	}
	// Include commands declared in frontmatter for pending (lazy) extensions.
	for _, pe := range pending {
		for _, cmd := range pe.cfg.Commands {
			cmds = append(cmds, ExtCommand{
				ExtName: pe.cfg.Name,
				Spec:    CommandSpec{Name: cmd.Name, Description: cmd.Description},
			})
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
					_ = json.Unmarshal(raw, &result)
				}
				return result, nil
			}
		}
	}

	// Check pending extensions for a matching frontmatter command.
	m.mu.Lock()
	var matchedCfg *ExtProcConfig
	var remaining []*pendingExtension
	for _, pe := range m.pending {
		matched := false
		for _, cmd := range pe.cfg.Commands {
			if cmd.Name == name {
				matched = true
				cfg := pe.cfg
				matchedCfg = &cfg
				break
			}
		}
		if !matched {
			remaining = append(remaining, pe)
		}
	}
	if matchedCfg != nil {
		m.pending = remaining
	}
	cwd := m.cwd
	sdkEnv := m.sdkEnv
	api := m.api
	projectDir := m.projectDir
	m.mu.Unlock()

	if matchedCfg != nil {
		// Start the extension, then dispatch the command.
		if err := m.startOne(context.Background(), *matchedCfg, cwd, sdkEnv, api, projectDir); err != nil {
			return CommandResult{}, fmt.Errorf("failed to start extension %s for command %q: %w", matchedCfg.Name, name, err)
		}
		m.logger.Info("lazy-started extension for command", "ext", matchedCfg.Name, "command", name)
		// Retry dispatch now that the extension is running.
		return m.DispatchCommand(name, args, timeout)
	}

	return CommandResult{}, fmt.Errorf("extension: no command %q registered", name)
}

// CollectSessionData returns a snapshot of every running extension's session
// data, keyed by extension name.  Used by /reexec to persist data in the
// sidecar file.
func (m *Manager) CollectSessionData() map[string]map[string]string {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	out := make(map[string]map[string]string)
	for _, mb := range bridges {
		if d := mb.bridge.GetAllSessionData(); len(d) > 0 {
			out[mb.cfg.Name] = d
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SeedSessionData pre-populates per-extension session data from a previously
// saved snapshot (e.g. a reexec sidecar).  Must be called before
// EmitSessionStartWithData so extensions receive the data in their session_start
// event params.
func (m *Manager) SeedSessionData(data map[string]map[string]string) {
	if len(data) == 0 {
		return
	}
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		if d, ok := data[mb.cfg.Name]; ok {
			mb.bridge.SeedSessionData(d)
		}
	}
}

// EmitSessionStartWithData emits "session_start" to every subscribed bridge.
// Each bridge receives its own saved session data (if any) in the event params
// under the key "session_data", so extensions can restore state in their
// session_start handler without a separate get_session_data call.
func (m *Manager) EmitSessionStartWithData(reexecData map[string]map[string]string) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		var params map[string]any
		if d, ok := reexecData[mb.cfg.Name]; ok && len(d) > 0 {
			params = map[string]any{"session_data": d}
		}
		_ = mb.bridge.EmitEvent("session_start", params)
	}
}
