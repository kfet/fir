// Package reexec provides SIGHUP-triggered graceful restart for all fir modes.
//
// When SIGHUP is received, the handler waits for all registered sessions
// to finish streaming (via event subscription), then performs a clean
// reexec: flush sessions, run shutdown callbacks, exec new binary.
package reexec

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/kfet/fir/pkg/agent"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/session"
)

// SessionInfo holds everything needed to cleanly reexec a session.
type SessionInfo struct {
	Session *session.AgentSession
	// OnShutdown is called before exec to save mode-specific state
	// (e.g. extension data, follow-up queue). May be nil.
	OnShutdown func()
}

// Handler manages SIGHUP-triggered reexec across any number of sessions.
type Handler struct {
	mu       sync.Mutex
	sessions []SessionInfo
}

// NewHandler creates a handler and starts listening for SIGHUP.
func NewHandler() *Handler {
	h := &Handler{}
	go h.listen()
	return h
}

// Register adds a session to be waited on before reexec.
func (h *Handler) Register(info SessionInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions = append(h.sessions, info)
}

// Unregister removes a session (by pointer equality).
func (h *Handler) Unregister(sess *session.AgentSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, info := range h.sessions {
		if info.Session == sess {
			h.sessions = append(h.sessions[:i], h.sessions[i+1:]...)
			return
		}
	}
}

func (h *Handler) listen() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	<-sigCh
	firlog.Info("reexec: SIGHUP received")

	h.mu.Lock()
	sessions := make([]SessionInfo, len(h.sessions))
	copy(sessions, h.sessions)
	h.mu.Unlock()

	// Wait for all sessions to finish streaming.
	WaitAllIdle(sessions)
	firlog.Info("reexec: all sessions idle")

	// Run shutdown callbacks and flush.
	for _, info := range sessions {
		if info.OnShutdown != nil {
			info.OnShutdown()
		}
		info.Session.SessionStore.ForceFlush()
	}

	// Exec with same binary and args.
	Exec("", nil)
}

// WaitAllIdle blocks until every session is no longer streaming,
// using event subscription for deterministic wakeup.
func WaitAllIdle(sessions []SessionInfo) {
	var wg sync.WaitGroup
	for _, info := range sessions {
		s := info.Session
		if s == nil || !s.IsStreaming() {
			continue
		}
		wg.Add(1)
		ch := make(chan struct{}, 1)
		unsub := s.Subscribe(func(ev session.AgentSessionEvent) {
			if ev.AgentEvent != nil && ev.AgentEvent.Type == agent.EventAgentEnd {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		})
		go func() {
			defer wg.Done()
			defer unsub()
			if !s.IsStreaming() {
				return
			}
			<-ch
		}()
	}
	wg.Wait()
}

// Exec performs the actual syscall.Exec. If binary is empty, uses the
// current executable. If args is nil, uses os.Args.
// This function never returns on success.
func Exec(binary string, args []string) {
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			firlog.Warn("reexec: cannot determine executable", "err", err)
			return
		}
	}
	if args == nil {
		args = os.Args
	}

	restoreStdinBlocking()

	env := append(os.Environ(), "FIR_REEXEC_CONTINUE=1")
	if err := syscall.Exec(binary, args, env); err != nil {
		firlog.Warn("reexec: exec failed", "err", err)
	}
}
