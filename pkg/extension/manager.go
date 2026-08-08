package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	stopped   bool // set true by Stop; startOne refuses to append after it
	ConfirmFn ConfirmFunc

	// reloadOneMu serializes ReloadOne calls so two concurrent reloads of
	// the same extension cannot both miss the running instance and spawn
	// duplicate bridges.
	reloadOneMu sync.Mutex

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
	startCtx            context.Context // lifetime ctx for (re)spawned bridges
	projectDir          string
	cwd                 string
	api                 BridgeAPI
	sdkEnv              []string // cached SDK env vars
	sdkDir              string   // extracted SDK directory
	extraExtensionDirs  []string // extra dirs from installed packages
	extraExtensionFiles []string // extra script files from installed packages
	configDirs          []string // priority-ordered config dirs sent to extensions on init

	// fork is the warm Python template used to COW-spawn builtin .py
	// extensions. It is a process-lifetime singleton shared across all
	// sessions/managers (see SharedForkServer), lazily acquired in Start
	// when at least one eligible extension exists. StopWithReason stops this
	// session's forked children but never closes the shared template. nil
	// when disabled, unavailable, or no eligible extensions are present.
	fork *ForkServer
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

// SetNotifyFn sets the notification callback applied to all new bridges
// AND propagates it to bridges already running. Safe to call before or
// after Start() — covers the common case where the host constructs the
// callback against UI state that only exists after the TUI has booted
// (interactive mode calls this from SetExtensionSetup, which runs after
// the bridges have already been started in a background goroutine).
func (m *Manager) SetNotifyFn(fn NotifyFunc) {
	m.mu.Lock()
	m.notifyFn = fn
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()
	for _, mb := range bridges {
		if mb != nil && mb.bridge != nil {
			mb.bridge.SetNotifyFn(fn)
		}
	}
}

// SetSetStatusFn sets the status callback applied to all new bridges
// AND propagates it to bridges already running. Safe to call before or
// after Start() — covers the common case where the host constructs the
// callback against UI state that only exists after the TUI has booted
// (interactive mode calls this from SetExtensionSetup, which runs after
// the bridges have already been started in a background goroutine).
func (m *Manager) SetSetStatusFn(fn SetStatusFunc) {
	m.mu.Lock()
	m.setStatusFn = fn
	bridges := append([]*managedBridge(nil), m.bridges...)
	m.mu.Unlock()
	for _, mb := range bridges {
		if mb != nil && mb.bridge != nil {
			mb.bridge.SetSetStatusFn(fn)
		}
	}
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
	m.startCtx = ctx
	m.stopped = false
	m.projectDir = projectDir
	m.cwd = cwd
	m.api = api
	m.mu.Unlock()

	// Wire the targeted single-extension reload callback so the
	// SessionBridge (which fields the inbound reload_extension RPC) can
	// delegate back into this manager. Mirrors the type-assertion seam
	// used by Reload to reach UnregisterExtensionTools.
	if r, ok := api.(interface {
		SetReloadFn(func(name string) error)
	}); ok {
		r.SetReloadFn(func(name string) error {
			m.mu.Lock()
			ctx := m.startCtx
			m.mu.Unlock()
			return m.ReloadOne(ctx, name)
		})
	}

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
		// Layer package-contributed extensions beneath discovered ones.
		// mergeConfigsByName enforces project/global shadowing by Name.
		if len(extraDirs) > 0 {
			if extraConfigs, extraErr := DiscoverExtra(extraDirs); extraErr == nil {
				configs = mergeConfigsByName(configs, extraConfigs)
			}
		}
		if len(extraFiles) > 0 {
			configs = mergeConfigsByName(configs, ConfigsFromFiles(extraFiles))
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

	// Acquire the process-wide warm fork template, before the parallel spawn
	// loop, if any extension is fork-eligible (builtin .py) and the template
	// is not disabled. The template is shared across sessions (keyed by SDK
	// dir, i.e. sdk-hash); builtin .py extensions COW-fork off it; everything
	// else (and any fork failure) falls back to the plain exec path in
	// startOne.
	if sdkPath != "" && !forkServerDisabled() {
		eligible := false
		for _, cfg := range toStart {
			if forkEligible(cfg) {
				eligible = true
				break
			}
		}
		if eligible {
			if fs, ferr := SharedForkServer(sdkPath, sdkEnv, m.logger); ferr != nil {
				m.logger.Warn("fork template unavailable; using exec spawn", "err", ferr)
			} else {
				m.mu.Lock()
				m.fork = fs
				m.mu.Unlock()
			}
		}
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
// allowlist, explicit-only, and mode filtering. Extracted from startOne so that
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
	// Explicit extensions are opt-in: they only load when named in the
	// allowlist (e.g. via `-e <name>`). Without an explicit allowlist match,
	// skip them.
	if cfg.Explicit && !containsString(allowed, cfg.Name) {
		return true
	}
	if !extensionSupportsMode(cfg.Modes, m.ActiveMode) {
		return true
	}
	return false
}

// startProc spawns proc, preferring the warm fork template for eligible builtin
// .py extensions and falling back to a plain exec spawn on any fork failure.
func (m *Manager) startProc(proc *Process, cfg ExtProcConfig) error {
	m.mu.Lock()
	fs := m.fork
	m.mu.Unlock()

	if fs != nil && forkEligible(cfg) {
		if err := proc.StartForked(fs); err != nil {
			m.logger.Warn("fork spawn failed; falling back to exec", "ext", cfg.Name, "err", err)
		} else {
			return nil
		}
	}
	return proc.Start()
}

func (m *Manager) startOne(ctx context.Context, cfg ExtProcConfig, cwd string, env []string, api BridgeAPI, projectDir string) error {
	startOneBegin := time.Now()
	// Reject extension names that collide with reserved core observable
	// sources (see docs/design/observable-cards.md "Ownership").
	if reservedSourceName(cfg.Name) {
		return fmt.Errorf("extension name %q collides with a reserved core source (reserved: %s); "+
			"rename the extension", cfg.Name, strings.Join(reservedSources, ", "))
	}
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
	if cfg.Explicit && !containsString(allowed, cfg.Name) {
		m.logger.Debug("skipping extension (explicit-only, not requested)", "ext", cfg.Name)
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
	if err := m.startProc(proc, cfg); err != nil {
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
	// Wire observable cards before handlers can fire (nil-safe).
	bridge.SetObservableStore(api.GetObservableStore())
	bridge.RegisterTools(api)
	// Apis come first: a Provider may declare api="<id>" referencing an
	// Api this same extension also ships, so the wire-protocol registry
	// must be populated before the provider record (which validates the
	// Api existence at registration time, indirectly via stream lookups).
	bridge.RegisterApis()
	// Provider record must land before auth-provider registration so that
	// any auth code paths reading ai.GetProviderRecord(<id>) (e.g. to look
	// up OAuthProviderID ↔ provider id mappings) see a populated record.
	// Reverse order on shutdown: tear down auth first.
	bridge.RegisterProviders()
	bridge.RegisterAuthProviders()
	// Register any static tool-name map declared by the extension so that
	// provider adapters (e.g. anthropic OAuth mode) can translate tool
	// names to and from the LLM. Collected once here; re-registered on
	// reload.
	if len(caps.ToolNameMap) > 0 {
		providers.RegisterToolNameAliases(caps.Name, caps.ToolNameMap)
	}
	m.logger.Info("ext startOne: done", "ext", cfg.Name, "total_ms", time.Since(startOneBegin).Milliseconds())

	// Wire optional UI callbacks AND insert into m.bridges atomically.
	// We must wire the bridge before starting its Run goroutine (otherwise
	// an inbound set_status / notify arriving immediately after handshake
	// would race past a nil pointer); and we must do the wire-then-append
	// under a single critical section so a concurrent SetSetStatusFn /
	// SetNotifyFn either sees this bridge in m.bridges (and updates it
	// directly) OR runs before we even read m.{setStatus,notify}Fn (in
	// which case we pick up its newly-written value here). Without that
	// invariant the bridge could land in m.bridges holding a stale nil
	// callback even though m.setStatusFn has already been updated.
	bCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	// If the manager was stopped between process spawn and here (e.g. the
	// session is shutting down while a reload respawns), do not register
	// this bridge — Stop() has already drained m.bridges and would never
	// tear this one down, orphaning the process. Roll it back instead.
	if m.stopped {
		m.mu.Unlock()
		cancel()
		bridge.UnregisterAuthProviders()
		bridge.UnregisterProviders()
		bridge.UnregisterApis()
		if len(caps.ToolNameMap) > 0 {
			providers.UnregisterToolNameAliases(caps.Name)
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = proc.Stop(stopCtx)
		stopCancel()
		return nil
	}
	if m.notifyFn != nil {
		bridge.SetNotifyFn(m.notifyFn)
	}
	if m.setStatusFn != nil {
		bridge.SetSetStatusFn(m.setStatusFn)
	}
	m.bridges = append(m.bridges, &managedBridge{
		cfg:    cfg,
		proc:   proc,
		bridge: bridge,
		cancel: cancel,
	})
	m.mu.Unlock()

	go func() {
		if err := bridge.Run(bCtx, api); err != nil && bCtx.Err() == nil {
			m.logger.Warn("bridge exited", "ext", cfg.Name, "err", err)
		}
	}()

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
	m.stopped = true
	m.fork = nil
	m.mu.Unlock()

	// Build session_end payload.
	payload := SessionEndPayload{Reason: reason}
	if errMsg != "" {
		payload.Error = errMsg
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
		mb.bridge.UnregisterProviders()
		mb.bridge.UnregisterApis()
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

	// Do NOT close the fork template: it is a process-lifetime singleton
	// shared across sessions (see SharedForkServer). This session's forked
	// children were stopped and reaped above (each proc.Stop delegated a
	// stop{pid} to the template); other sessions' children — and the warm
	// template itself — stay up.
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
		m.EmitEvent("session_named", SessionNamedPayload{Name: name})
	}
	return nil
}

// reloadGrace is the brief window ReloadOne waits after emitting an
// extension's session_end before signalling its process, giving the
// extension's dispatch loop a chance to service the event. Best-effort,
// mirrors Stop's 250ms grace; a package var so tests can shrink it.
var reloadGrace = 100 * time.Millisecond

// ReloadOne reloads exactly one extension by name — the targeted counterpart
// to Reload. An extension that created or edited another extension mid-session
// can refresh just that one, without tearing down every other running
// extension.
//
// Entirely internal, in order:
//  1. Re-discover extensions and locate the cfg whose Name == name.
//  2. Builtins are never reloadable; if the located cfg is builtin, return an
//     error and touch nothing.
//  3. If an extension with that name is currently running, emit its
//     session_end, remove ONLY its tools (other extensions' tools are left
//     intact), tear down its provider/auth/api registrations, cancel its
//     context, stop its process, and drop it from m.bridges.
//  4. If the cfg was found in discovery, startOne it to (re)spawn the
//     edited/new version. If it was NOT found but an instance was running
//     (the file was deleted mid-session), this is a stop-only unload and
//     succeeds. If it was NOT found and nothing was running, nothing happened
//     at all, so ReloadOne returns an error naming the extension and listing
//     the names that were discovered.
func (m *Manager) ReloadOne(ctx context.Context, name string) error {
	// Serialize reloads so two concurrent ReloadOne(name) calls cannot both
	// miss the running instance and spawn duplicate bridges.
	m.reloadOneMu.Lock()
	defer m.reloadOneMu.Unlock()

	m.mu.Lock()
	projectDir := m.projectDir
	cwd := m.cwd
	api := m.api
	sdkEnv := m.sdkEnv
	m.mu.Unlock()

	if projectDir == "" || api == nil {
		return fmt.Errorf("manager was not started; cannot reload extension %q", name)
	}

	// 1. Re-discover and locate the target cfg.
	configs, err := Discover(projectDir)
	if err != nil {
		return err
	}
	var found *ExtProcConfig
	for i := range configs {
		if configs[i].Name == name {
			cfg := configs[i]
			found = &cfg
			break
		}
	}

	// 2. Builtins are never reloadable.
	if found != nil && found.Scope == "builtin" {
		return fmt.Errorf("extension %q is builtin and cannot be reloaded", name)
	}

	// 3. Stop the running instance (if any) and remove only its tools.
	m.mu.Lock()
	var running *managedBridge
	kept := make([]*managedBridge, 0, len(m.bridges))
	for _, mb := range m.bridges {
		if mb.cfg.Name == name {
			running = mb
		} else {
			kept = append(kept, mb)
		}
	}
	m.bridges = kept
	m.mu.Unlock()

	if running != nil {
		// Let the extension clean up before we tear it down. The grace
		// gives its dispatch loop a brief window to service the event
		// before the process is signalled (best-effort, like Stop).
		_ = running.bridge.EmitEvent("session_end", SessionEndPayload{Reason: "reload"})
		_ = running.bridge.EmitEvent("session_shutdown", nil)
		if reloadGrace > 0 {
			time.Sleep(reloadGrace)
		}

		// Remove ONLY this extension's tools from the session's tool set.
		// The bridge already knows its own tool names from caps.Tools; we
		// reuse the SessionBridge's ToolSet.Remove path internally without
		// exposing a caller-visible "unregister by name" API.
		if running.bridge != nil && running.bridge.caps != nil {
			var toolNames []string
			for _, t := range running.bridge.caps.Tools {
				toolNames = append(toolNames, t.Name)
			}
			if len(toolNames) > 0 {
				if r, ok := api.(interface {
					removeExtensionTools(names []string)
				}); ok {
					r.removeExtensionTools(toolNames)
				}
			}
		}

		// Tear down provider/auth/api registrations (mirrors Stop) and
		// cancel the bridge context, then stop the process.
		running.bridge.UnregisterAuthProviders()
		running.bridge.UnregisterProviders()
		running.bridge.UnregisterApis()
		if running.bridge.caps != nil && len(running.bridge.caps.ToolNameMap) > 0 {
			providers.UnregisterToolNameAliases(running.bridge.caps.Name)
		}
		running.cancel()
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = running.proc.Stop(stopCtx)
		cancel()
	}

	// 4. Respawn the edited/new version, or stop-only if the file was deleted.
	if found == nil {
		if running == nil {
			// Nothing was discovered and nothing was running: this call did
			// nothing at all, so reporting success would be a lie.
			names := make([]string, 0, len(configs))
			for i := range configs {
				names = append(names, configs[i].Name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return fmt.Errorf("extension %q not found in discovery (found: none)", name)
			}
			return fmt.Errorf("extension %q not found in discovery (found: %s)", name, strings.Join(names, ", "))
		}
		// Genuine stop-only unload: it was running, its file is gone.
		m.logger.Info("extension unloaded (no longer discovered)", "ext", name)
		return nil
	}
	return m.startOne(ctx, *found, cwd, sdkEnv, api, projectDir)
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
	// Markdown, when true (and PrintResponse is true), renders Message as
	// markdown inside a high-contrast accent-bordered box rather than flat
	// muted text. Use for prose responses (e.g. /advise). Leave false for
	// preformatted/whitespace-aligned output (e.g. tables) where markdown
	// would collapse alignment.
	Markdown bool `json:"markdown"`
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
				params := CommandHookPayload{
					Name: name,
					Args: args,
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
	fork := m.fork
	m.mu.Unlock()

	out := make([]int, 0, len(bridges)+1)
	for _, mb := range bridges {
		if pid := mb.proc.Pid(); pid > 0 {
			out = append(out, pid)
		}
	}
	// Include the fork template itself so /reexec SIGKILLs it too (it is a
	// direct child of this process; its forked children get reparented and
	// exit on socket EOF).
	if fork != nil {
		if pid := fork.Pid(); pid > 0 {
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
		_ = mb.bridge.EmitEvent("session_end", SessionEndPayload{Reason: "reexec"})
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
		var payload *SessionStartPayload
		// Always include session_id so extensions can identify the session.
		var sid string
		if m.api != nil {
			sid = m.api.GetSessionID()
		}
		var data map[string]string
		if d, ok := reexecData[mb.cfg.Name]; ok && len(d) > 0 {
			mb.bridge.SeedSessionData(d)
			data = d
		}
		if sid != "" || len(data) > 0 {
			payload = &SessionStartPayload{
				SessionID:   sid,
				SessionData: data,
			}
		}
		_ = mb.bridge.EmitEvent("session_start", payload)
	}
}

// ---------------------------------------------------------------------------
// Reserved observable-card source names
// ---------------------------------------------------------------------------

// reservedSources are Card.Source values owned by core. Extensions
// cannot claim these names (matched case-insensitively); the trust
// seam in bridge.go stamps source from cfg.Name, so a "plan" extension
// would otherwise impersonate the plan tool's cards.
var reservedSources = []string{"plan", "model", "session"}

// reservedSourceName reports whether name collides with a reserved
// core source (case-insensitive).
func reservedSourceName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, r := range reservedSources {
		if lower == r {
			return true
		}
	}
	return false
}
