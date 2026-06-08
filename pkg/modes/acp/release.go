package acp

import (
	"context"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// reaperInterval is the maximum interval between idle-session reaper passes.
// The effective interval also scales down with the TTL (see reaperIntervalFor)
// so a small TTL is reaped promptly.
const reaperInterval = time.Minute

// reaperIntervalFor returns how often the reaper should wake for the given
// idle TTL: at most reaperInterval, but no coarser than half the TTL, and
// never below one second. This keeps a 1h TTL on a 1-minute cadence while a
// few-second TTL (used in tests) is polled every second.
func reaperIntervalFor(ttl time.Duration) time.Duration {
	iv := reaperInterval
	if half := ttl / 2; half < iv {
		iv = half
	}
	if iv < time.Second {
		iv = time.Second
	}
	return iv
}

// teardownSession tears down a single session and releases all resources it
// holds: pending/background ACP terminals, the event subscription, the agent
// session, its extension sidecars, and its MCP subprocess tree.
//
// The caller MUST have already removed the entry from pa.sessions (under
// pa.mu) before calling this. teardownSession blocks (it waits for async
// extension setup and stops subprocesses) and must therefore run OUTSIDE
// pa.mu so it never serialises unrelated session operations.
func (pa *firAgent) teardownSession(ctx context.Context, sessionID string, entry *firSession) {
	if entry == nil {
		return
	}
	CleanupPendingBashTerminals(ctx, pa.conn, entry.termState, sessionID)
	CleanupBackgroundTerminals(ctx, pa.conn, entry.termState, sessionID)
	if entry.unsubscribe != nil {
		entry.unsubscribe()
	}
	if entry.session != nil {
		entry.session.Close()
	}
	// Wait for async extension setup to finish before shutting it down, so
	// EmitSessionStart and EmitSessionShutdown can't race on Manager state.
	if entry.extReady != nil {
		<-entry.extReady
	}
	if entry.extSetup != nil {
		entry.extSetup.EmitSessionShutdown()
	}
	if entry.mcpManager != nil {
		_ = entry.mcpManager.Close()
	}
}

// removeSession atomically removes sessionID from the map and returns the
// entry (or nil, false if absent). It does NOT tear down — the caller must
// call teardownSession on the returned entry outside the lock.
func (pa *firAgent) removeSession(sessionID string) (*firSession, bool) {
	pa.mu.Lock()
	entry, ok := pa.sessions[sessionID]
	if ok {
		delete(pa.sessions, sessionID)
	}
	pa.mu.Unlock()
	return entry, ok
}

// ReleaseSession handles the session/release method. It tears down and forgets
// the named in-memory session, freeing its extension sidecars and MCP
// subprocesses. The on-disk session file is left intact (it can be resumed
// later). Returns the typed session-not-found error if the session is unknown.
func (pa *firAgent) ReleaseSession(ctx context.Context, params ReleaseSessionRequest) (ReleaseSessionResponse, error) {
	entry, ok := pa.removeSession(params.SessionId)
	if !ok {
		// Not in memory — but it may have been idle-reaped, leaving a record
		// that a later Prompt would re-hydrate from. An explicit release is
		// authoritative: forget that record so the session is truly gone.
		if _, reaped := pa.takeReaped(params.SessionId); reaped {
			firlog.Info("acp session/release: forgot reaped session", "sessionId", params.SessionId)
			return ReleaseSessionResponse{}, nil
		}
		return ReleaseSessionResponse{}, newSessionNotFound(params.SessionId)
	}
	firlog.Info("acp session/release: tearing down", "sessionId", params.SessionId)
	pa.teardownSession(ctx, params.SessionId, entry)
	return ReleaseSessionResponse{}, nil
}

// reapIdle tears down every session whose last activity is older than the
// idle TTL, measured against now. It returns the IDs that were reaped.
// A non-positive idleTTL disables reaping (returns nil).
func (pa *firAgent) reapIdle(now time.Time) []string {
	if pa.idleTTL <= 0 {
		return nil
	}
	cutoff := now.Add(-pa.idleTTL)

	// Collect victims under the lock, then tear down outside it.
	pa.mu.Lock()
	var victims []string
	for sid, entry := range pa.sessions {
		if entry.lastActive().Before(cutoff) {
			victims = append(victims, sid)
		}
	}
	entries := make([]*firSession, 0, len(victims))
	for _, sid := range victims {
		entries = append(entries, pa.sessions[sid])
		delete(pa.sessions, sid)
	}
	pa.mu.Unlock()

	for i, sid := range victims {
		firlog.Info("acp idle reaper: tearing down idle session",
			"sessionId", sid, "idleSeconds", now.Sub(entries[i].lastActive()).Seconds())
		// Remember where this session's transcript lives (and its cwd) so a
		// later Prompt can re-hydrate it in place under the same sessionID,
		// rather than surfacing session-not-found. Capture before teardown
		// closes the session.
		pa.rememberReaped(sid, entries[i])
		pa.teardownSession(context.Background(), sid, entries[i])
	}
	return victims
}

// rememberReaped records a reaped session's on-disk transcript path and cwd,
// keyed by sessionID, so a subsequent Prompt can re-hydrate it under the same
// ID. Safe to call with a nil entry.
func (pa *firAgent) rememberReaped(sessionID string, entry *firSession) {
	if entry == nil {
		return
	}
	var file string
	if entry.session != nil && entry.session.SessionStore != nil {
		file = entry.session.SessionStore.GetSessionFile()
	}
	pa.mu.Lock()
	if pa.reaped == nil {
		pa.reaped = make(map[string]reapedSession)
	}
	pa.reaped[sessionID] = reapedSession{file: file, cwd: entry.cwd}
	pa.mu.Unlock()
}

// takeReaped atomically looks up and removes the reaped record for sessionID.
func (pa *firAgent) takeReaped(sessionID string) (reapedSession, bool) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	r, ok := pa.reaped[sessionID]
	if ok {
		delete(pa.reaped, sessionID)
	}
	return r, ok
}

// restoreReaped re-inserts a reaped record removed by takeReaped, used when a
// re-hydration attempt failed so a later retry can still recover the session.
// It never clobbers a newer record (e.g. one written by a subsequent re-reap).
func (pa *firAgent) restoreReaped(sessionID string, r reapedSession) {
	pa.mu.Lock()
	if pa.reaped == nil {
		pa.reaped = make(map[string]reapedSession)
	}
	if _, exists := pa.reaped[sessionID]; !exists {
		pa.reaped[sessionID] = r
	}
	pa.mu.Unlock()
}

// startIdleReaper launches the background goroutine that periodically reaps
// idle sessions. It is a no-op when idleTTL is non-positive (reaper disabled).
func (pa *firAgent) startIdleReaper(interval time.Duration) {
	if pa.idleTTL <= 0 {
		return
	}
	pa.stopReaper = make(chan struct{})
	pa.reaperDone = make(chan struct{})

	go func() {
		defer close(pa.reaperDone)

		tickCh := pa.reaperTick
		var ticker *time.Ticker
		if tickCh == nil {
			ticker = time.NewTicker(interval)
			defer ticker.Stop()
			tickCh = ticker.C
		}

		for {
			select {
			case <-pa.stopReaper:
				return
			case <-tickCh:
				pa.reapIdle(pa.now())
				if pa.reaperNotify != nil {
					select {
					case pa.reaperNotify <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
}

// stopIdleReaper signals the reaper goroutine to exit and waits for it. Safe
// to call when the reaper was never started (no-op).
func (pa *firAgent) stopIdleReaper() {
	if pa.stopReaper == nil {
		return
	}
	close(pa.stopReaper)
	<-pa.reaperDone
	pa.stopReaper = nil
}
