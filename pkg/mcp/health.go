package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Health states of a configured MCP server.
const (
	StateOK           = "ok"
	StateConnecting   = "connecting"
	StateNeedsAuth    = "needs_auth"
	StateTokenExpired = "token_expired"
	StateError        = "error"
	StateDisabled     = "disabled"
)

// ServerHealth is the per-server health record: what the agent and
// front-ends need to know about a configured server without its tools.
type ServerHealth struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Source string `json:"source,omitempty"` // config file that defined it ("" = session/flag)
	Error  string `json:"error,omitempty"`
	Tools  int    `json:"tools"`
	Fix    string `json:"fix,omitempty"` // fir command that resolves the state
}

// Healthy reports whether the state needs no user action.
func (h ServerHealth) Healthy() bool {
	return h.State == StateOK || h.State == StateConnecting
}

// healthWatch holds the change-detection state for Health.
type healthWatch struct {
	mu       sync.Mutex // serialises compute+compare+notify so callbacks stay ordered
	last     []ServerHealth
	sources  map[string]string
	onChange func([]ServerHealth)
}

// SetSources records which config file defined each server, for Health.
func (m *Manager) SetSources(src map[string]string) {
	m.health.mu.Lock()
	m.health.sources = src
	m.health.mu.Unlock()
	m.checkHealth()
}

// SetOnHealthChanged installs a callback fired with the full health list
// whenever any server's health record changes (state, error, tool count).
// Calls are serialised and ordered. fn must return quickly and must not call
// back into the Manager (it runs under the health lock). Set it before Start.
func (m *Manager) SetOnHealthChanged(fn func([]ServerHealth)) {
	m.health.mu.Lock()
	m.health.onChange = fn
	m.health.mu.Unlock()
}

// Health returns the health of every configured server, sorted by name.
func (m *Manager) Health() []ServerHealth {
	m.health.mu.Lock()
	src := m.health.sources
	m.health.mu.Unlock()
	return m.computeHealth(src)
}

func (m *Manager) computeHealth(src map[string]string) []ServerHealth {
	var out []ServerHealth
	m.forEachServer(func(name string, e *serverEntry) bool {
		h := ServerHealth{Name: name, Source: src[name], Tools: len(e.tools)}
		var authErr *AuthRequiredError
		if v, ok := m.auths.Load(name); ok {
			authErr = v.(*serverAuth).lastRequired.Load()
		}
		if authErr == nil && e.err != nil {
			errors.As(e.err, &authErr)
		}
		switch {
		case e.config.Disabled:
			h.State = StateDisabled
		case authErr != nil:
			h.State = StateNeedsAuth
			if authErr.Reason != reasonNoCredential {
				h.State = StateTokenExpired
			}
			h.Error = authErr.Error()
		case e.session != nil:
			h.State = StateOK
		case e.err != nil:
			h.State = StateError
			h.Error = e.err.Error()
		default:
			h.State = StateConnecting
		}
		h.Fix = fixFor(h)
		out = append(out, h)
		return true
	})
	slices.SortFunc(out, func(a, b ServerHealth) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func fixFor(h ServerHealth) string {
	switch h.State {
	case StateNeedsAuth, StateTokenExpired:
		return "/mcp login " + h.Name
	case StateError:
		return "/mcp reload " + h.Name
	case StateDisabled:
		where := h.Source
		if where == "" {
			where = "its MCP config"
		}
		return fmt.Sprintf("none (remove \"disabled\" in %s, then /mcp reload %s)", where, h.Name)
	}
	return ""
}

// checkHealth recomputes health and fires the change callback if anything
// differs from the last notified snapshot.
func (m *Manager) checkHealth() {
	w := &m.health
	w.mu.Lock()
	defer w.mu.Unlock()
	cur := m.computeHealth(w.sources)
	if slices.Equal(cur, w.last) {
		return
	}
	w.last = cur
	if w.onChange != nil {
		w.onChange(cur)
	}
}

// stateLabel is the human label used in the agent-facing section.
func stateLabel(state string) string {
	switch state {
	case StateNeedsAuth:
		return "NOT AUTHENTICATED"
	case StateTokenExpired:
		return "TOKEN EXPIRED"
	case StateDisabled:
		return "DISABLED"
	}
	return "ERROR"
}

// maxErrLen bounds the error excerpt in a section line.
const maxErrLen = 160

// RenderHealthSection renders one line per unhealthy server, or "" when
// every server is healthy. Front-end agnostic: it names fir commands only.
func RenderHealthSection(hs []ServerHealth) string {
	var b strings.Builder
	for _, h := range hs {
		if h.Healthy() {
			continue
		}
		fmt.Fprintf(&b, "MCP server %s: configured, %s, tools unavailable -> fir command: %s", h.Name, stateLabel(h.State), h.Fix)
		if h.State == StateNeedsAuth || h.State == StateTokenExpired {
			fmt.Fprintf(&b, " (if logged in elsewhere: /mcp reload %s)", h.Name)
		}
		if h.State == StateError && h.Error != "" {
			msg := strings.Join(strings.Fields(h.Error), " ")
			if len(msg) > maxErrLen {
				msg = msg[:maxErrLen] + "…"
			}
			fmt.Fprintf(&b, " [%s]", msg)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
