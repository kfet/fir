package mcp

import (
	"context"
	"errors"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// credWatchInterval is how often a started Manager checks auth.json for a
// credential written by another process. Variable so tests can change it.
var credWatchInterval = 5 * time.Second

// startCredentialWatch launches, once per Manager, a goroutine that notices
// credentials minted out-of-band — `fir mcp login <server>` in a shell while
// a relay or headless session is running — and re-dials servers blocked on a
// login, so the user does not also have to send /mcp reload.
//
// The check is a stat of auth.json; nothing is re-read unless it changed.
func (m *Manager) startCredentialWatch() {
	if !m.credStore.persistent() {
		return
	}
	m.credWatch.Do(func() {
		m.reconnectWG.Add(1)
		go func() {
			defer m.reconnectWG.Done()
			t := time.NewTicker(credWatchInterval)
			defer t.Stop()
			for {
				select {
				case <-m.done:
					return
				case <-t.C:
					m.pollCredentials()
				}
			}
		}()
	})
}

// pollCredentials re-dials auth-blocked servers that now have a credential.
// It does not gate on ReloadIfStale reporting a change: request paths in this
// process call it too and may already have consumed the change.
func (m *Manager) pollCredentials() {
	if !m.credStore.persistent() {
		return
	}
	m.credStore.reloadIfStale()
	var names []string
	m.forEachServer(func(name string, _ *serverEntry) bool {
		names = append(names, name)
		return true
	})
	for _, name := range names {
		m.redialIfCredentialed(context.Background(), name)
	}
}

// redialIfCredentialed re-dials a server that is down for lack of a login once
// a usable credential for it is in the store. A server with a parked
// reconnect loop is kicked; one whose initial connect failed (no loop) is
// started synchronously. Reports whether a re-dial was triggered.
func (m *Manager) redialIfCredentialed(ctx context.Context, name string) bool {
	var (
		cfg                 ServerConfig
		blocked, hasLoop    bool
		connected, disabled bool
		connecting          bool
		kick                chan struct{}
	)
	if !m.withEntry(name, func(e *serverEntry) {
		cfg = e.config
		var authErr *AuthRequiredError
		blocked = e.err != nil && errors.As(e.err, &authErr)
		hasLoop = e.reconnectCancel != nil
		connected = e.session != nil
		connecting = e.connecting
		disabled = e.config.Disabled
		kick = e.kick
	}) || disabled || connected || connecting {
		return false
	}
	v, ok := m.auths.Load(name)
	if !ok {
		return false
	}
	sa := v.(*serverAuth)
	if !blocked && sa.lastRequired.Load() == nil {
		return false
	}
	if m.credStore.Load(name, sa.resource) == nil {
		return false
	}
	firlog.Info("mcp oauth: credential appeared in store; reconnecting", "server", name)
	if hasLoop {
		select {
		case kick <- struct{}{}:
		default:
		}
		return true
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	select {
	case <-m.done:
		return false
	default:
	}
	// A concurrent caller may have connected it while we waited for reloadMu.
	var busy bool
	m.withEntry(name, func(e *serverEntry) {
		busy = e.session != nil || e.connecting || e.reconnectCancel != nil
	})
	if busy {
		return true
	}
	// The session outlives the caller (a tool call's ctx), like Start's.
	_, err := m.startServer(context.WithoutCancel(ctx), name, cfg)
	m.withEntry(name, func(e *serverEntry) { e.err = err })
	if notify := m.loadOnToolsChanged(); notify != nil {
		notify(m.allTools())
	}
	m.emitServerEvent(ServerEvent{Kind: ServerReady, Name: name, Err: err})
	m.checkHealth()
	return true
}
