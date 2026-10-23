package session

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/sections"
)

// MCPSectionName owns the core section listing unhealthy MCP servers.
const MCPSectionName = "mcp"

// mcpInitialWait bounds how long the first LLM call waits for MCP servers to
// finish their initial connect, so the startup section reflects real state
// (e.g. a server that answers 401) rather than "still connecting".
var mcpInitialWait = 5 * time.Second

// mcpHealthState tracks the session's MCP manager for health reporting.
type mcpHealthState struct {
	mgr      atomic.Pointer[mcp.Manager]
	waitOnce sync.Once

	mu       sync.Mutex
	subs     map[int]func([]mcp.ServerHealth)
	next     int
	pending  []mcp.ServerHealth // latest undelivered snapshot
	hasNew   bool               // pending is set (it may legitimately be nil)
	draining bool               // a delivery goroutine is running
}

// attachMCP points health reporting at mgr (nil detaches).
func (s *AgentSession) attachMCP(mgr *mcp.Manager) {
	s.mcp.mgr.Store(mgr)
	if mgr == nil {
		return
	}
	mgr.SetOnHealthChanged(s.publishMCPHealth)
	mgr.SetSources(mcp.ConfigSources(s.cwd))
}

// MCPHealth returns the health of every configured MCP server (nil when no
// MCP manager is attached).
func (s *AgentSession) MCPHealth() []mcp.ServerHealth {
	if mgr := s.mcp.mgr.Load(); mgr != nil {
		return mgr.Health()
	}
	return nil
}

// SubscribeMCPHealth registers fn to receive the full health list whenever
// any server's health changes. Returns an unsubscribe func.
func (s *AgentSession) SubscribeMCPHealth(fn func([]mcp.ServerHealth)) func() {
	st := &s.mcp
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.subs == nil {
		st.subs = map[int]func([]mcp.ServerHealth){}
	}
	id := st.next
	st.next++
	st.subs[id] = fn
	return func() {
		st.mu.Lock()
		delete(st.subs, id)
		st.mu.Unlock()
	}
}

// publishMCPHealth hands h to subscribers without blocking the caller (the
// MCP manager calls it under its health lock, from connect/reconnect paths).
// A single short-lived goroutine delivers in order; snapshots superseded
// before delivery are skipped, since each one is the full list.
func (s *AgentSession) publishMCPHealth(h []mcp.ServerHealth) {
	st := &s.mcp
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pending, st.hasNew = h, true
	if !st.draining {
		st.draining = true
		go s.drainMCPHealth()
	}
}

func (s *AgentSession) drainMCPHealth() {
	st := &s.mcp
	for {
		st.mu.Lock()
		if !st.hasNew {
			st.draining = false
			st.mu.Unlock()
			return
		}
		h := st.pending
		st.pending, st.hasNew = nil, false
		fns := make([]func([]mcp.ServerHealth), 0, len(st.subs))
		for _, fn := range st.subs {
			fns = append(fns, fn)
		}
		st.mu.Unlock()
		for _, fn := range fns {
			fn(h)
		}
	}
}

// mcpSections is the dynamic section provider: one line per unhealthy MCP
// server, nothing when all are healthy. The first call waits (bounded) for
// initial connects so the very first turn sees real state.
func (s *AgentSession) mcpSections() []sections.Section {
	mgr := s.mcp.mgr.Load()
	if mgr == nil {
		return nil
	}
	s.mcp.waitOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), mcpInitialWait)
		defer cancel()
		_ = mgr.WaitReady(ctx)
	})
	text := sections.Sanitize(mcp.RenderHealthSection(mgr.Health()))
	if text == "" {
		return nil
	}
	return []sections.Section{{Name: MCPSectionName, Text: text}}
}
