package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/extension/sdk"
	"github.com/kfet/fir/pkg/resources"
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

	// DisabledNames is an optional denylist of extension names. Extensions
	// whose Name appears in this list are skipped even if in AllowedNames.
	DisabledNames []string

	// ActiveMode is the currently running fir mode (interactive, text, json, rpc, acp).
	// Extensions with mode constraints that do not include this mode are skipped.
	ActiveMode string

	// Optional UI callbacks, applied to each bridge when it starts.
	notifyFn    NotifyFunc
	setStatusFn SetStatusFunc

	// startFailures records extensions that failed during Start.
	startFailures []StartFailure

	// Saved from Start() for Reload().
	projectDir          string
	cwd                 string
	api                 BridgeAPI
	sdkEnv              []string // cached SDK env vars
	sdkDir              string   // extracted SDK directory
	extraExtensionDirs  []string // extra dirs from installed packages
	extraExtensionFiles []string // extra script files from installed packages
	configDirs          []string // priority-ordered config dirs sent to extensions on init
}

type managedBridge struct {
	cfg    ExtProcConfig
	proc   *Process
	bridge *Bridge
	cancel context.CancelFunc
}

// StartFailure records an extension that failed to start.
type StartFailure struct {
	Name   string // extension name
	IsAuth bool   // true when the extension provides auth providers
	Err    error  // the error that prevented startup
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

// SetDisabledNames updates the optional extension denylist.
// Extensions in this list are skipped even if in AllowedNames.
func (m *Manager) SetDisabledNames(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DisabledNames = append([]string(nil), names...)
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

// SetConfigDirs sets the priority-ordered list of directories sent to each
// extension during the init handshake. Highest priority first (e.g.
// [projectDir/.fir, ~/.config/fir]). Call before Start.
func (m *Manager) SetConfigDirs(dirs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configDirs = append([]string(nil), dirs...)
}

// Start discovers extensions, spawns processes, performs handshakes, and
// starts bridge dispatch loops.  All discovered extensions start eagerly in
// parallel — there is no lazy/deferred startup.
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
	m.sdkDir = sdkPath
	m.mu.Unlock()

	// All discovered extensions start eagerly, in parallel.  Handshakes spawn
	// Python interpreters and are I/O-bound, so parallelism keeps wall-clock
	// startup low (~150ms total for ~9 builtins on M-series).
	var toStart []ExtProcConfig
	for _, cfg := range configs {
		if m.shouldSkip(cfg) {
			continue
		}
		toStart = append(toStart, cfg)
	}

	var wg sync.WaitGroup
	var failMu sync.Mutex
	var failures []StartFailure
	for _, cfg := range toStart {
		wg.Add(1)
		go func(cfg ExtProcConfig) {
			defer wg.Done()
			if err := m.startOne(ctx, cfg, cwd, sdkEnv, api, projectDir); err != nil {
				m.logger.Warn("failed to start extension",
					"ext", cfg.Name, "err", err)
				failMu.Lock()
				failures = append(failures, StartFailure{
					Name:   cfg.Name,
					IsAuth: len(cfg.AuthProviders) > 0,
					Err:    err,
				})
				failMu.Unlock()
			}
		}(cfg)
	}
	wg.Wait()

	m.mu.Lock()
	m.startFailures = failures
	m.mu.Unlock()

	// Resolve auth provider ID collisions deterministically by scope priority.
	m.resolveAuthConflicts()

	return nil
}

// shouldSkip returns true if the extension should be skipped based on
// allowlist, demo, and mode filtering. Extracted from startOne so that
// Start can partition before spawning goroutines.
func (m *Manager) shouldSkip(cfg ExtProcConfig) bool {
	m.mu.Lock()
	allowed := m.AllowedNames
	disabled := m.DisabledNames
	m.mu.Unlock()
	if containsString(disabled, cfg.Name) {
		return true
	}
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
	startOneBegin := time.Now()
	// Allowlist check: skip extensions not in AllowedNames when the list is set.
	m.mu.Lock()
	allowed := m.AllowedNames
	disabled := m.DisabledNames
	m.mu.Unlock()
	if containsString(disabled, cfg.Name) {
		m.logger.Debug("skipping extension (disabled)", "ext", cfg.Name)
		return nil
	}
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
	t0 := time.Now()
	if err := proc.Start(); err != nil {
		return err
	}
	m.logger.Info("ext startOne: process started", "ext", cfg.Name, "elapsed_ms", time.Since(t0).Milliseconds())

	t0 = time.Now()
	caps, err := Handshake(proc, cwd, m.configDirs, 0)
	if err != nil {
		_ = proc.Stop(context.Background())
		return err
	}
	m.logger.Info("ext startOne: handshake done", "ext", cfg.Name, "elapsed_ms", time.Since(t0).Milliseconds())

	// Reject extension commands that clash with builtins or other extensions.
	if err := m.checkCommandClashes(cfg.Name, caps.Commands); err != nil {
		_ = proc.Stop(context.Background())
		return err
	}

	bridge := NewBridge(proc, caps)
	bridge.RegisterTools(api)
	bridge.RegisterAuthProviders()
	// Register any static tool-name map declared by the extension so that
	// provider adapters (e.g. anthropic OAuth mode) can translate tool
	// names to and from the LLM. Collected once here; re-registered on
	// reload.
	if len(caps.ToolNameMap) > 0 {
		providers.RegisterToolNameAliases(caps.Name, caps.ToolNameMap)
	}
	m.logger.Info("ext startOne: done", "ext", cfg.Name, "total_ms", time.Since(startOneBegin).Milliseconds())

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
// Sends session_end event before stopping.
func (m *Manager) Stop() error {
	return m.StopWithReason("normal", "")
}

// StopWithReason shuts down all bridges, emitting a session_end event that
// includes the exit reason and optional error message.
func (m *Manager) StopWithReason(reason, errMsg string) error {
	m.mu.Lock()
	bridges := m.bridges
	m.bridges = nil
	m.mu.Unlock()

	// Build session_end payload.
	payload := map[string]any{
		"reason": reason,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}

	// Send session_end event to all extensions concurrently so slow
	// extensions on low-powered hardware (e.g. RPi) don't serialise the
	// shutdown path.  Also send legacy session_shutdown for compat.
	var wg sync.WaitGroup
	for _, mb := range bridges {
		wg.Add(1)
		go func(mb *managedBridge) {
			defer wg.Done()
			_ = mb.bridge.EmitEvent("session_end", payload)
			_ = mb.bridge.EmitEvent("session_shutdown", nil)
		}(mb)
	}
	wg.Wait()

	// Brief grace period so extensions can process the shutdown event.
	time.Sleep(250 * time.Millisecond)

	// Stop all processes concurrently — each Stop already has its own
	// SIGTERM→SIGKILL escalation with timeouts, so parallel teardown is
	// safe and avoids O(n) sequential waits on slow hardware.
	for _, mb := range bridges {
		mb.bridge.UnregisterAuthProviders()
		if mb.bridge != nil && mb.bridge.caps != nil && len(mb.bridge.caps.ToolNameMap) > 0 {
			providers.UnregisterToolNameAliases(mb.bridge.caps.Name)
		}
		mb.cancel()
	}
	for _, mb := range bridges {
		wg.Add(1)
		go func(mb *managedBridge) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = mb.proc.Stop(ctx)
			cancel()
		}(mb)
	}
	wg.Wait()
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
	if err := m.Start(ctx, projectDir, cwd, api); err != nil {
		return err
	}

	// Re-emit session_named so reloaded extensions pick up the current name.
	if name := api.GetSessionName(); name != "" {
		m.EmitEvent("session_named", map[string]any{"name": name})
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

// CallHook calls all bridges with the given hook and collects results concurrently.
func (m *Manager) CallHook(ctx context.Context, name string, data any, timeout time.Duration) ([]json.RawMessage, error) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	var (
		wg      sync.WaitGroup
		resMu   sync.Mutex
		results []json.RawMessage
	)

	for _, mb := range bridges {
		wg.Add(1)
		go func(mb *managedBridge) {
			defer wg.Done()
			raw, err := mb.bridge.CallHook(ctx, name, data, timeout)
			if err != nil {
				m.logger.Warn("hook call failed", "ext", mb.cfg.Name, "hook", name, "err", err)
				return
			}
			if raw != nil {
				resMu.Lock()
				results = append(results, raw)
				resMu.Unlock()
			}
		}(mb)
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

// scopePriority returns a numeric priority for an extension scope.
// Higher values win when two extensions register the same auth provider ID.
func scopePriority(scope string) int {
	switch scope {
	case "builtin":
		return 0
	case "package":
		return 1
	case "global":
		return 2
	case "project":
		return 3
	default:
		return -1
	}
}

// resolveAuthConflicts detects auth provider ID collisions across running
// bridges and keeps only the highest-scope-priority registration for each ID.
// Lower-priority duplicates are unregistered from the global oauth registry
// and a warning is logged. Must be called with m.mu NOT held.
func (m *Manager) resolveAuthConflicts() {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	notifyFn := m.notifyFn
	m.mu.Unlock()

	// Map auth provider ID → best bridge so far.
	type winner struct {
		mb       *managedBridge
		priority int
	}
	best := make(map[string]*winner)

	for _, mb := range bridges {
		if mb.bridge == nil || mb.bridge.caps == nil {
			continue
		}
		pri := scopePriority(mb.cfg.Scope)
		for _, ap := range mb.bridge.caps.AuthProviders {
			w, exists := best[ap.ID]
			if !exists {
				best[ap.ID] = &winner{mb: mb, priority: pri}
				continue
			}
			// Conflict — keep higher priority, evict the other.
			var loser *managedBridge
			if pri > w.priority {
				loser = w.mb
				best[ap.ID] = &winner{mb: mb, priority: pri}
			} else if pri == w.priority {
				// Same scope — deterministic tie-break by extension name.
				if mb.cfg.Name < w.mb.cfg.Name {
					loser = w.mb
					best[ap.ID] = &winner{mb: mb, priority: pri}
				} else {
					loser = mb
				}
				m.logger.Warn("auth provider conflict: two same-scope extensions register the same ID; keeping alphabetically first",
					"auth_provider", ap.ID,
					"scope", mb.cfg.Scope,
					"kept", best[ap.ID].mb.cfg.Name,
					"dropped", loser.cfg.Name,
				)
				if notifyFn != nil {
					notifyFn("warning", fmt.Sprintf(
						"Auth provider %q conflict: extensions %q and %q (both %s scope) register the same ID. Keeping %q.",
						ap.ID, best[ap.ID].mb.cfg.Name, loser.cfg.Name, mb.cfg.Scope, best[ap.ID].mb.cfg.Name,
					))
				}
			} else {
				loser = mb
				m.logger.Warn("auth provider conflict: higher-scope extension wins",
					"auth_provider", ap.ID,
					"kept", best[ap.ID].mb.cfg.Name,
					"dropped", loser.cfg.Name,
				)
			}
			// Unregister the loser's registration from the global registry.
			loser.bridge.UnregisterAuthProvider(ap.ID)
			// Re-register the winner to ensure it owns the slot, since
			// the concurrent RegisterProvider calls may have overwritten it.
			best[ap.ID].mb.bridge.ReregisterAuthProvider(ap.ID)
		}
	}
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
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ExtensionToolNames returns a map of extension name → tool names for all
// running extensions that have registered tools.
func (m *Manager) ExtensionToolNames() map[string][]string {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	result := make(map[string][]string)
	for _, mb := range bridges {
		if mb.bridge == nil || mb.bridge.caps == nil {
			continue
		}
		name := mb.bridge.caps.Name
		for _, t := range mb.bridge.caps.Tools {
			result[name] = append(result[name], t.Name)
		}
	}
	return result
}

// ManagerPaths holds the filesystem paths used by the extension manager.
type ManagerPaths struct {
	SDKDir string // extracted SDK directory (empty if extraction failed)
}

// Paths returns the filesystem paths used by the extension manager.
func (m *Manager) Paths() ManagerPaths {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ManagerPaths{SDKDir: m.sdkDir}
}

// StartFailures returns extensions that failed during the most recent Start
// or Reload. The slice is cleared on each Start call.
func (m *Manager) StartFailures() []StartFailure {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]StartFailure(nil), m.startFailures...)
}

// checkCommandClashes returns an error if any command in cmds conflicts with a
// builtin slash command or a command already registered by another extension.
// Must be called before adding the bridge to m.bridges.
func (m *Manager) checkCommandClashes(extName string, cmds []CommandSpec) error {
	for _, cmd := range cmds {
		if resources.IsBuiltinSlashCommandName(cmd.Name) {
			return fmt.Errorf("extension %q: command /%s conflicts with a built-in slash command", extName, cmd.Name)
		}
	}

	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	existing := make(map[string]string) // command name -> owning extension
	for _, mb := range bridges {
		for _, spec := range mb.bridge.caps.Commands {
			existing[spec.Name] = mb.cfg.Name
		}
	}

	for _, cmd := range cmds {
		if owner, ok := existing[cmd.Name]; ok {
			return fmt.Errorf("extension %q: command /%s conflicts with command from extension %q", extName, cmd.Name, owner)
		}
	}
	return nil
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
	// PrintResponse, when true, prints Message to the main conversation area
	// (scrollable message list) instead of the transient status bar. Use this
	// for commands that return substantial output (e.g. advisor responses).
	PrintResponse bool `json:"print_response"`
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
				raw, err := mb.bridge.CallHook(context.Background(), "hook/command", params, timeout)
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

	return CommandResult{}, fmt.Errorf("extension: no command %q registered", name)
}

// ExtensionPIDs returns the OS PIDs of all currently running extension
// subprocesses. Used by /reexec to record which children syscall.Exec will
// orphan, so the post-exec process can SIGKILL them when the builtin
// extension hash changes (see store.ReexecSidecar).
func (m *Manager) ExtensionPIDs() []int {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	out := make([]int, 0, len(bridges))
	for _, mb := range bridges {
		if pid := mb.proc.Pid(); pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}

// CollectSessionData returns a snapshot of every running extension's session
// data, keyed by extension name.  Used by /reexec to persist data in the
// sidecar file.  Returns nil when no extension has stored any data.
func (m *Manager) CollectSessionData() map[string]map[string]string {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	var out map[string]map[string]string
	for _, mb := range bridges {
		if d := mb.bridge.GetAllSessionData(); len(d) > 0 {
			if out == nil {
				out = make(map[string]map[string]string)
			}
			out[mb.cfg.Name] = d
		}
	}
	return out
}

// ShutdownAndCollect is the /reexec equivalent of Stop+CollectSessionData.
// It fires "session_shutdown" to every running extension so their handlers
// can call ctx.set_session_data(...), waits for a grace period so the
// bridge's dispatch loop can service those inbound RPC calls, and then
// returns a snapshot of all per-extension session data.
//
// Unlike Stop, it does NOT kill the extension processes — the caller
// (handleReexecCommand) shuts down the TUI immediately after, and
// syscall.Exec replaces the process entirely, so no explicit teardown is
// needed.
func (m *Manager) ShutdownAndCollect() map[string]map[string]string {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		_ = mb.bridge.EmitEvent("session_end", map[string]any{"reason": "reexec"})
		_ = mb.bridge.EmitEvent("session_shutdown", nil)
	}

	// Give extensions time to run their session_shutdown handlers and make
	// the resulting set_session_data RPC calls back to us.  The bridge's
	// Run goroutine is still alive at this point (the context has not been
	// cancelled yet), so it can service those inbound requests.
	time.Sleep(500 * time.Millisecond)

	return m.CollectSessionData()
}

// SeedSessionData pre-populates per-extension session data from a previously
// saved snapshot (e.g. a reexec sidecar).  Note: EmitSessionStartWithData
// already calls Bridge.SeedSessionData internally, so this method is only
// needed when seeding data without emitting an event.
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
// The data is also seeded into each bridge's store so ctx.get_session_data()
// works throughout the session.
func (m *Manager) EmitSessionStartWithData(reexecData map[string]map[string]string) {
	m.mu.Lock()
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()

	for _, mb := range bridges {
		var params map[string]any
		if d, ok := reexecData[mb.cfg.Name]; ok && len(d) > 0 {
			mb.bridge.SeedSessionData(d)
			params = map[string]any{"session_data": d}
		}
		_ = mb.bridge.EmitEvent("session_start", params)
	}
}
