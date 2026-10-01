package acp

import (
	"context"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

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
	// A released session has no in-flight work left to report on.
	pa.stopSessionHeartbeats(sessionID)
	return entry, ok
}

// ReleaseSession handles the session/release method. It tears down and forgets
// the named in-memory session, freeing its extension sidecars and MCP
// subprocesses. The on-disk session file is left intact (it can be resumed
// later). Returns the typed session-not-found error if the session is unknown.
func (pa *firAgent) ReleaseSession(ctx context.Context, params ReleaseSessionRequest) (ReleaseSessionResponse, error) {
	entry, ok := pa.removeSession(params.SessionId)
	if !ok {
		// Not in memory — but it may have been idle-reaped, leaving a
		// binding a later Prompt would rehydrate from. An explicit release is
		// authoritative: forget it so the session is truly gone.
		if pa.forgetSession(params.SessionId) {
			firlog.Info("acp session/release: forgot reaped session", "sessionId", params.SessionId)
			return ReleaseSessionResponse{}, nil
		}
		return ReleaseSessionResponse{}, newSessionNotFound(params.SessionId)
	}
	firlog.Info("acp session/release: tearing down", "sessionId", params.SessionId)
	pa.teardownSession(ctx, params.SessionId, entry)
	pa.forgetSession(params.SessionId)
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
	pa.pruneHandles(now.Add(-handleMaxAge))

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
		pa.stopSessionHeartbeats(sid)
		firlog.Info("acp idle reaper: tearing down idle session",
			"sessionId", sid, "idleSeconds", now.Sub(entries[i].lastActive()).Seconds())
		// Save the session's state so a later Prompt rehydrates it in place
		// through openSession. Capture before teardown closes the session.
		if entries[i].session != nil {
			entries[i].session.SaveState()
		}
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

// CloseSession handles the spec's session/close method, added to the Agent
// interface in acp-go-sdk v0.13. Its contract — cancel any ongoing work and
// free the session's resources — is exactly what fir's older, unstable
// session/release already did, so this delegates rather than duplicating the
// teardown. Both entry points remain: clients that speak session/release keep
// working, and spec-conformant clients get session/close.
func (pa *firAgent) CloseSession(ctx context.Context, params acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	_, err := pa.ReleaseSession(ctx, ReleaseSessionRequest{SessionId: string(params.SessionId)})
	if err != nil {
		return acpsdk.CloseSessionResponse{}, err
	}
	return acpsdk.CloseSessionResponse{}, nil
}
