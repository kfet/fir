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
		pa.teardownSession(context.Background(), sid, entries[i])
	}
	return victims
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
