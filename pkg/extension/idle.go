package extension

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Idle stop: after the session has sat idle for idleAfter, the manager stops
// its forked extension children (the shared fork template stays warm) and
// re-forks them on the next user activity, before the turn starts. A resumed
// extension sees the same events as after /reexec: session_end{reason:"idle"}
// on stop, then session_start carrying the session data it had stored, then
// session_named. Idle stops are deliberate, so they never count toward the
// crash respawn budget (handleBridgeDied ignores bridges no longer tracked).

// IdleStopReason is the session_end reason sent to extensions stopped while idle.
const IdleStopReason = "idle"

// idledExt is an extension stopped by the idle stop, waiting for Resume.
type idledExt struct {
	cfg      ExtProcConfig
	data     map[string]string
	commands []CommandSpec
}

// ResumeStats reports how long Resume took: per extension (fork + handshake +
// registration) and in total (including the session_start acknowledgement).
type ResumeStats struct {
	PerExt map[string]time.Duration
	Total  time.Duration
	Failed []string
}

// idleStoppable reports whether mb may be stopped while idle: only forked
// children, never a keep_alive extension, and never one that serves auth,
// providers or apis (other code paths can call those between turns).
func idleStoppable(mb *managedBridge) bool {
	if mb.cfg.KeepAlive || len(mb.cfg.AuthProviders) > 0 || !mb.proc.Forked() {
		return false
	}
	if c := mb.bridge.caps; c != nil && (len(c.AuthProviders) > 0 || len(c.Providers) > 0 || len(c.Apis) > 0) {
		return false
	}
	return true
}

// SetIdleStop sets how long the session must sit idle before forked extension
// children are stopped. Zero or negative turns the idle stop off.
func (m *Manager) SetIdleStop(d time.Duration) {
	m.mu.Lock()
	m.idleAfter = d
	m.armIdleLocked()
	m.mu.Unlock()
}

// SetBusy marks whether a turn is running. The idle clock starts when the
// session goes idle and stops while it is busy.
func (m *Manager) SetBusy(busy bool) {
	m.mu.Lock()
	m.busy = busy
	m.armIdleLocked()
	m.mu.Unlock()
}

// armIdleLocked (re)starts the idle timer. A generation counter voids timers
// that fired while being replaced. Caller holds m.mu.
func (m *Manager) armIdleLocked() {
	m.idleGen++
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
	if m.idleAfter <= 0 || m.busy || m.stopped {
		return
	}
	gen := m.idleGen
	m.idleTimer = time.AfterFunc(m.idleAfter, func() {
		m.mu.Lock()
		current := gen == m.idleGen
		m.mu.Unlock()
		if current {
			m.IdleStop()
		}
	})
}

// IdleStop stops every idle-stoppable extension now and returns their names.
// It does nothing while a turn runs or after Stop.
func (m *Manager) IdleStop() []string {
	m.reloadOneMu.Lock()
	defer m.reloadOneMu.Unlock()

	m.mu.Lock()
	if m.busy || m.stopped {
		m.mu.Unlock()
		return nil
	}
	var victims []*managedBridge
	kept := make([]*managedBridge, 0, len(m.bridges))
	for _, mb := range m.bridges {
		if idleStoppable(mb) {
			victims = append(victims, mb)
		} else {
			kept = append(kept, mb)
		}
	}
	m.bridges = kept
	api := m.api
	m.mu.Unlock()
	if len(victims) == 0 {
		return nil
	}

	awaitShutdownEvents(victims, SessionEndPayload{Reason: IdleStopReason})

	entries := make([]idledExt, len(victims))
	var wg sync.WaitGroup
	for i, mb := range victims {
		entries[i] = idledExt{cfg: mb.cfg, data: mb.bridge.GetAllSessionData(), commands: mb.bridge.caps.Commands}
		wg.Add(1)
		go func(mb *managedBridge) {
			defer wg.Done()
			m.teardownBridge(api, mb)
		}(mb)
	}
	wg.Wait()

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.cfg.Name
	}
	sort.Strings(names)
	m.mu.Lock()
	m.idled = append(m.idled, entries...)
	m.mu.Unlock()
	m.logger.Info("extensions stopped while idle", "count", len(names), "exts", names)
	return names
}

// IdleStopped returns the names of extensions currently stopped while idle.
func (m *Manager) IdleStopped() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.idled))
	for i, e := range m.idled {
		names[i] = e.cfg.Name
	}
	sort.Strings(names)
	return names
}

// Resume re-forks the extensions stopped while idle and replays session_start
// (with their saved session data) and session_named, so their state matches
// what they had before. It is cheap when nothing is idle-stopped, and restarts
// the idle clock either way.
func (m *Manager) Resume() ResumeStats {
	m.reloadOneMu.Lock()
	defer m.reloadOneMu.Unlock()

	m.mu.Lock()
	entries := m.idled
	m.idled = nil
	ctx, cwd, env, api, projectDir := m.startCtx, m.cwd, m.sdkEnv, m.api, m.projectDir
	stopped := m.stopped
	m.armIdleLocked()
	m.mu.Unlock()
	if len(entries) == 0 || stopped {
		return ResumeStats{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	begin := time.Now()
	stats := ResumeStats{PerExt: make(map[string]time.Duration, len(entries))}
	var statMu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e idledExt) {
			defer wg.Done()
			t0 := time.Now()
			err := m.startOne(ctx, e.cfg, cwd, env, api, projectDir)
			statMu.Lock()
			defer statMu.Unlock()
			if err != nil {
				m.logger.Error("resuming idle-stopped extension failed", "ext", e.cfg.Name, "err", err)
				stats.Failed = append(stats.Failed, e.cfg.Name)
				return
			}
			stats.PerExt[e.cfg.Name] = time.Since(t0)
		}(e)
	}
	wg.Wait()

	data := make(map[string]map[string]string, len(entries))
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.cfg.Name] = true
		if len(e.data) > 0 {
			data[e.cfg.Name] = e.data
		}
	}
	m.mu.Lock()
	var resumed []*managedBridge
	for _, mb := range m.bridges {
		if names[mb.cfg.Name] {
			resumed = append(resumed, mb)
		}
	}
	m.mu.Unlock()
	m.emitSessionStart(resumed, data)
	if api != nil {
		if name := api.GetSessionName(); name != "" {
			for _, mb := range resumed {
				_ = mb.bridge.EmitEvent("session_named", SessionNamedPayload{Name: name})
			}
		}
	}
	stats.Total = time.Since(begin)
	sort.Strings(stats.Failed)
	perExtMs := make(map[string]int64, len(stats.PerExt))
	for n, d := range stats.PerExt {
		perExtMs[n] = d.Milliseconds()
	}
	m.logger.Info("extensions resumed after idle", "count", len(stats.PerExt), "total_ms", stats.Total.Milliseconds(), "per_ext_ms", perExtMs, "failed", stats.Failed)
	return stats
}
