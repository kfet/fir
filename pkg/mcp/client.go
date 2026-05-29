package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpLevelToSlog maps MCP LoggingLevel strings to the corresponding slog level.
// Unknown levels default to slog.LevelDebug.
var mcpLevelToSlog = map[sdk.LoggingLevel]slog.Level{
	"debug":     slog.LevelDebug,
	"info":      slog.LevelInfo,
	"notice":    (slog.LevelInfo + slog.LevelWarn) / 2,
	"warning":   slog.LevelWarn,
	"error":     slog.LevelError,
	"critical":  slog.LevelError + 4,
	"alert":     slog.LevelError + 8,
	"emergency": slog.LevelError + 12,
}

// progressRegistry maps progress tokens to active tool-call update callbacks.
type progressRegistry struct {
	m sync.Map // string → agent.AgentToolUpdateCallback
}

func (r *progressRegistry) register(token string, cb agent.AgentToolUpdateCallback) {
	r.m.Store(token, cb)
}

func (r *progressRegistry) dispatch(token string, result agent.AgentToolResult) {
	if v, ok := r.m.Load(token); ok {
		v.(agent.AgentToolUpdateCallback)(result)
	}
}

// serverEntry holds all per-server state in a single struct, stored in
// Manager.servers as a sync.Map value. Fields are guarded by mu;
// subscribed has its own lock-free sync.Map and does not require mu.
type serverEntry struct {
	mu         sync.Mutex
	config     ServerConfig
	session    *sdk.ClientSession      // nil while connecting or after disconnect
	tools      []agent.AgentTool       // tools exposed by this server
	err        error                   // last connection/disconnect error
	connecting bool                    // true while initial connect is in progress
	caps       *sdk.ServerCapabilities // cached after Connect; read by ToolListChangedHandler
	subscribed sync.Map                // uri (string) → struct{}

	// Auto-reconnect state. Channels are created once at entry construction
	// and live for the entry's lifetime; the ready field is reassigned (a
	// fresh open chan) on every disconnect so each connection generation
	// has its own waitable signal.
	//
	//   ready  — closed when a session is currently installed; replaced
	//            with a fresh open chan on disconnect. CallTool snapshots
	//            this chan to wait for reconnect.
	//   kick   — buffered(1). Non-blocking send wakes the reconnect loop
	//            from its backoff sleep so on-demand CallTool gets fast
	//            recovery instead of waiting for the next timer tick.
	//   reconnectCancel — cancels the reconnect loop's ctx (Close/Reload).
	//   attempt — consecutive dial-failure counter. Drives backoff and
	//             the "surface error after N failures" UX.
	ready           chan struct{}
	kick            chan struct{}
	reconnectCancel context.CancelFunc
	attempt         int
}

// with locks the entry, calls fn, and unlocks.
func (e *serverEntry) with(fn func(e *serverEntry)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e)
}

// forEachServer iterates all server entries, locking each for the duration
// of fn. Iteration stops if fn returns false.
func (m *Manager) forEachServer(fn func(name string, e *serverEntry) bool) {
	m.servers.Range(func(key, value any) bool {
		entry := value.(*serverEntry)
		var cont bool
		entry.with(func(e *serverEntry) {
			cont = fn(key.(string), e)
		})
		return cont
	})
}

// ServerEventKind classifies an MCP server lifecycle transition.
type ServerEventKind int

const (
	// ServerConnecting is emitted when a server begins a connection attempt:
	// once for the initial dial, and once at the start of each reconnect
	// cycle following a disconnect (not for retries within a cycle).
	ServerConnecting ServerEventKind = iota

	// ServerReady is emitted when a server's session becomes active — both
	// for the initial connection and for each successful reconnect. On an
	// initial-connect failure it is emitted with a non-nil Err (reconnect
	// failures do not emit it).
	ServerReady

	// ServerDisconnected is emitted when an active server session terminates
	// unexpectedly (not via a clean Close/Reload). Err is the wait error
	// wrapped as "disconnected: ...", or nil if the session ended without
	// one (e.g. server-initiated EOF).
	ServerDisconnected
)

// ServerEvent reports an MCP server lifecycle transition. Events for a single
// server are delivered in order on the channel returned by Manager.ServerEvents.
type ServerEvent struct {
	Kind ServerEventKind
	Name string
	Err  error // set for ServerReady (initial-connect failure) and ServerDisconnected
}

// serverEventBuffer bounds the lifecycle-event channel. It only needs to hold
// the burst emitted between Start and the consumer attaching; once draining it
// empties quickly. Sized generously relative to realistic server counts.
const serverEventBuffer = 64

// Manager owns the lifecycle of all MCP client sessions for one fir session.
type Manager struct {
	servers  sync.Map // string → *serverEntry
	verbose  bool
	reloadMu sync.Mutex // serialises concurrent Reload calls

	// onToolsChanged is called (from a background goroutine) whenever any
	// server's tool list changes. The argument is the new complete tool list
	// across all servers. May be nil.
	onToolsChanged atomic.Value // func([]agent.AgentTool)

	// onResourceUpdated is called when a subscribed resource is updated on a
	// server. serverName is the Manager key; uri is the resource that changed.
	// May be nil.
	onResourceUpdated atomic.Value // func(serverName, uri string)

	// serverEvents carries per-server lifecycle transitions (connecting,
	// ready, disconnected) to a single consumer obtained via ServerEvents.
	// It is buffered so events emitted before a consumer attaches — e.g. the
	// initial "connecting" event fired during session setup, before the TUI
	// that surfaces it has been built — are retained rather than dropped.
	// Emitters never block: a full buffer drops the event (see emitServerEvent).
	serverEvents chan ServerEvent

	// done is closed exactly once by Close to signal consumers (ServerEvents
	// readers) to stop. serverEvents itself is never closed because
	// background goroutines (in-flight Start, reconnect loops) may still emit
	// during shutdown; closing it would risk a send-on-closed panic.
	done      chan struct{}
	closeOnce sync.Once

	// onChannelMessage is called when a channel-capable MCP server sends a
	// notifications/claude/channel notification. May be nil.
	onChannelMessage atomic.Value // func(ChannelMessage)

	// SamplingFn is called when an MCP server issues a sampling/createMessage
	// request (asking fir to call an LLM). If nil, sampling requests are rejected.
	// Use NewSamplingFn to create a standard implementation.
	SamplingFn func(context.Context, *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error)

	// ElicitationFn is called when an MCP server requests user input via
	// elicitation/create. If nil, all elicitation requests are declined.
	// Use DefaultElicitFn for headless sessions or provide an interactive
	// implementation for UI-enabled sessions.
	ElicitationFn func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)

	// progressReg routes progress notifications to active tool-call callbacks.
	progressReg progressRegistry

	// dialFn creates a Transport for the given server config.
	// Defaults to commandTransport. Replaced in tests to inject in-memory transports.
	dialFn func(cfg ServerConfig) (sdk.Transport, error)

	// startWG tracks in-flight initial connection attempts started by Start.
	// WaitReady blocks until every goroutine launched by Start has finished
	// its initial connect (success or failure).
	startWG sync.WaitGroup

	// reconnectWG tracks in-flight reconnect-loop goroutines. Close waits
	// on this WG so callers know all loops have observed cancellation
	// before Close returns.
	reconnectWG sync.WaitGroup
}

// NewManager creates a new Manager for the given server configs.
// When verbose is true, MCP server log messages are forwarded at DEBUG level
// and the logging level sent to each server is "debug"; otherwise only
// warnings and above are requested.
func NewManager(configs map[string]ServerConfig, verbose bool) *Manager {
	mgr := &Manager{
		verbose:      verbose,
		dialFn:       createTransport,
		serverEvents: make(chan ServerEvent, serverEventBuffer),
		done:         make(chan struct{}),
	}
	for k, v := range configs {
		mgr.servers.Store(k, newServerEntry(v))
	}
	return mgr
}

// newServerEntry constructs an entry with reconnect channels initialised.
// ready is open (callers waiting on it block); kick has capacity 1 so a
// non-blocking send is always safe.
func newServerEntry(cfg ServerConfig) *serverEntry {
	return &serverEntry{
		config: cfg,
		ready:  make(chan struct{}),
		kick:   make(chan struct{}, 1),
	}
}

// loadEntry returns the serverEntry for name, or nil.
func (m *Manager) loadEntry(name string) *serverEntry {
	if v, ok := m.servers.Load(name); ok {
		return v.(*serverEntry)
	}
	return nil
}

// withEntry looks up the entry for name, locks it, calls fn, and unlocks.
// Returns false if the entry does not exist.
func (m *Manager) withEntry(name string, fn func(e *serverEntry)) bool {
	entry := m.loadEntry(name)
	if entry == nil {
		return false
	}
	entry.with(fn)
	return true
}

// loadOnToolsChanged returns the current onToolsChanged callback, or nil.
func (m *Manager) loadOnToolsChanged() func([]agent.AgentTool) {
	if v := m.onToolsChanged.Load(); v != nil {
		return v.(func([]agent.AgentTool))
	}
	return nil
}

// loadOnChannelMessage returns the current onChannelMessage callback, or nil.
func (m *Manager) loadOnChannelMessage() func(ChannelMessage) {
	if v := m.onChannelMessage.Load(); v != nil {
		return v.(func(ChannelMessage))
	}
	return nil
}

// ServerEvents returns the channel on which MCP server lifecycle events are
// delivered. There is a single logical consumer; events are buffered so those
// emitted before the consumer attaches are retained. The channel is never
// closed — a consumer must also select on Done to stop:
//
//	for {
//		select {
//		case <-mgr.Done():
//			return
//		case ev := <-mgr.ServerEvents():
//			// handle ev
//		}
//	}
func (m *Manager) ServerEvents() <-chan ServerEvent { return m.serverEvents }

// Done returns a channel closed when the Manager is closed. Consumers of
// ServerEvents select on it to stop draining.
func (m *Manager) Done() <-chan struct{} { return m.done }

// emitServerEvent delivers a lifecycle event to the ServerEvents channel
// without ever blocking the caller: these emit sites run inside connection
// and reconnect goroutines that must not stall. If the buffer is full the
// event is dropped (logged), which only happens when no consumer is draining.
func (m *Manager) emitServerEvent(ev ServerEvent) {
	select {
	case m.serverEvents <- ev:
	default:
		firlog.Warn("mcp server-event buffer full; dropping event",
			"kind", ev.Kind, "server", ev.Name)
	}
}

// loadOnResourceUpdated returns the current onResourceUpdated callback, or nil.
func (m *Manager) loadOnResourceUpdated() func(string, string) {
	if v := m.onResourceUpdated.Load(); v != nil {
		return v.(func(string, string))
	}
	return nil
}

// SetOnToolsChanged sets the callback invoked when any server's tool list
// changes. Safe to call concurrently with running servers.
func (m *Manager) SetOnToolsChanged(fn func([]agent.AgentTool)) {
	m.onToolsChanged.Store(fn)
}

// SetOnResourceUpdated sets the callback invoked when a subscribed resource
// is updated. Safe to call concurrently with running servers.
func (m *Manager) SetOnResourceUpdated(fn func(serverName, uri string)) {
	m.onResourceUpdated.Store(fn)
}

// SetOnChannelMessage sets the callback invoked when a channel-capable MCP
// server sends a notification. Safe to call concurrently with running servers.
func (m *Manager) SetOnChannelMessage(fn func(ChannelMessage)) {
	m.onChannelMessage.Store(fn)
}

// configsLen returns the number of configured servers.
func (m *Manager) configsLen() int {
	n := 0
	m.servers.Range(func(_, _ any) bool { n++; return true })
	return n
}

// configsSnapshot returns a plain map of server name → ServerConfig.
func (m *Manager) configsSnapshot() map[string]ServerConfig {
	out := make(map[string]ServerConfig)
	m.forEachServer(func(name string, e *serverEntry) bool {
		out[name] = e.config
		return true
	})
	return out
}

// allTools returns a flat snapshot of all tools across all servers.
func (m *Manager) allTools() []agent.AgentTool {
	var out []agent.AgentTool
	m.forEachServer(func(_ string, e *serverEntry) bool {
		out = append(out, e.tools...)
		return true
	})
	return out
}

// hasSession reports whether the named server has an active session.
// Intended for tests; production code should use withEntry.
func (m *Manager) hasSession(name string) bool {
	var connected bool
	m.withEntry(name, func(e *serverEntry) { connected = e.session != nil })
	return connected
}

// createTransport builds a Transport from a ServerConfig based on the
// Transport field. Supported values: "stdio" (default when empty), "sse",
// "streamable".
func createTransport(cfg ServerConfig) (sdk.Transport, error) {
	switch cfg.Transport {
	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("url is required for sse transport")
		}
		return &sdk.SSEClientTransport{Endpoint: cfg.URL}, nil
	case "streamable":
		if cfg.URL == "" {
			return nil, fmt.Errorf("url is required for streamable transport")
		}
		return &sdk.StreamableClientTransport{Endpoint: cfg.URL}, nil
	default: // "stdio" or ""
		if cfg.Transport != "" && cfg.Transport != "stdio" {
			return nil, fmt.Errorf("unsupported transport %q; valid values: stdio, sse, streamable", cfg.Transport)
		}
		return commandTransport(cfg)
	}
}

// commandTransport builds a CommandTransport from a ServerConfig.
func commandTransport(cfg ServerConfig) (sdk.Transport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...) // #nosec G204 — command from user config
	// Inherit the current process environment so the subprocess has PATH, HOME, etc.
	// then overlay any user-specified overrides.
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return &sdk.CommandTransport{Command: cmd}, nil
}

// Start launches async connections to all configured MCP servers. It returns
// immediately. As each server finishes connecting, its tools are stored and
// OnToolsChanged is called with the aggregate tool list. Failures are recorded
// and logged but do not prevent other servers from starting.
func (m *Manager) Start(ctx context.Context) {
	firlog.Info("mcp starting", "servers", m.configsLen())
	m.forEachServer(func(name string, e *serverEntry) bool {
		e.connecting = true
		cfg := e.config
		m.startWG.Add(1)
		go func() {
			defer m.startWG.Done()
			_, err := m.startServer(ctx, name, cfg)
			m.withEntry(name, func(e *serverEntry) {
				e.connecting = false
			})
			if err != nil {
				firlog.Warn("mcp connection failed", "server", name, "err", err)
				var sess *sdk.ClientSession
				m.withEntry(name, func(e *serverEntry) {
					e.err = err
					if e.session != nil {
						sess = e.session
						e.session = nil
						e.tools = nil
					}
				})
				if sess != nil {
					_ = sess.Close()
				}
			}
			if notify := m.loadOnToolsChanged(); notify != nil {
				notify(m.allTools())
			}
			m.emitServerEvent(ServerEvent{Kind: ServerReady, Name: name, Err: err})
		}()
		return true
	})
}

// loggingLevel returns the MCP logging level to request from servers.
func (m *Manager) loggingLevel() sdk.LoggingLevel {
	if m.verbose {
		return "debug"
	}
	return "warning"
}

// subscribeOnce returns a subscribeFunc that subscribes to a resource URI at
// most once per server. It uses the serverEntry's subscribed map.
func (m *Manager) subscribeOnce(session *sdk.ClientSession, serverName string) subscribeFunc {
	return func(uri string) {
		entry := m.loadEntry(serverName)
		if entry == nil {
			return
		}
		if _, already := entry.subscribed.LoadOrStore(uri, struct{}{}); !already {
			_ = session.Subscribe(context.Background(), &sdk.SubscribeParams{URI: uri})
		}
	}
}

// startServer connects to a single MCP server, installs the resulting
// session/tools/caps into the entry, and starts the auto-reconnect loop.
// Used for the initial Start path; the reconnect loop handles subsequent
// re-dials internally without re-entering this function.
func (m *Manager) startServer(ctx context.Context, name string, cfg ServerConfig) ([]agent.AgentTool, error) {
	firlog.Debug("mcp connecting", "server", name, "command", cfg.Command)
	m.emitServerEvent(ServerEvent{Kind: ServerConnecting, Name: name})
	session, tools, caps, err := m.dialAndInitialize(ctx, name, cfg)
	if err != nil {
		return nil, err
	}
	var ready chan struct{}
	m.withEntry(name, func(e *serverEntry) {
		e.session = session
		e.tools = tools
		e.caps = caps
		e.err = nil
		e.attempt = 0
		// Ensure ready is open so we can close it to signal "session
		// installed". If it was already closed (re-entry from Reload after
		// a previous successful connect), allocate a fresh chan first.
		select {
		case <-e.ready:
			e.ready = make(chan struct{})
		default:
		}
		ready = e.ready
	})
	close(ready)
	firlog.Info("mcp connected", "server", name, "tools", len(tools))

	// Start (or restart) the auto-reconnect loop for this server. The loop
	// owns post-install lifecycle: it waits for session.Wait() to return,
	// then transparently reconnects with backoff. CallTool can also kick
	// the loop on demand to skip the backoff sleep.
	m.startReconnectLoop(name)

	return tools, nil
}

// clientOptions builds the *sdk.ClientOptions for a server. Captured here
// rather than inline so both startServer and dialAndInitialize use the same
// handler set and the reconnect path doesn't drift from the initial connect.
func (m *Manager) clientOptions(serverName string) *sdk.ClientOptions {
	return &sdk.ClientOptions{
		LoggingMessageHandler: func(_ context.Context, req *sdk.LoggingMessageRequest) {
			p := req.Params
			level, ok := mcpLevelToSlog[p.Level]
			if !ok {
				level = slog.LevelDebug
			}
			attrs := []any{
				slog.String("mcp_server", serverName),
			}
			if p.Logger != "" {
				attrs = append(attrs, slog.String("logger", p.Logger))
			}
			if p.Data != nil {
				attrs = append(attrs, slog.Any("data", p.Data))
			}
			slog.Log(context.Background(), level, "MCP server log", attrs...)
		},
		ToolListChangedHandler: func(_ context.Context, req *sdk.ToolListChangedRequest) {
			if req.Session == nil {
				return
			}
			session := req.Session
			go func() {
				// If the session isn't installed in the entry yet, skip:
				// dialAndInitialize lists tools right after Connect anyway.
				var stored bool
				m.withEntry(serverName, func(e *serverEntry) { stored = e.session != nil })
				if !stored {
					return
				}
				var updated []agent.AgentTool
				for tool, err := range session.Tools(context.Background(), nil) {
					if err != nil {
						firlog.Warn("MCP re-list tools error", "server", serverName, "err", err)
						return
					}
					updated = append(updated, AdaptTool(m.serverSession(serverName), serverName, tool, &m.progressReg))
				}
				var caps *sdk.ServerCapabilities
				m.withEntry(serverName, func(e *serverEntry) {
					caps = e.caps
				})
				if caps != nil && caps.Resources != nil {
					updated = append(updated,
						listResourcesTool(session, serverName),
						readResourceTool(session, serverName, m.subscribeOnce(session, serverName)),
					)
				}
				if caps != nil && caps.Prompts != nil {
					updated = append(updated,
						listPromptsTool(session, serverName),
						getPromptTool(session, serverName),
					)
				}
				m.withEntry(serverName, func(e *serverEntry) {
					// Guard against stale updates: only overwrite tools if
					// this session is still the active session for this
					// server. Reload/reconnect may have replaced it.
					if e.session != session {
						return
					}
					e.tools = updated
				})
				if notify := m.loadOnToolsChanged(); notify != nil {
					notify(m.allTools())
				}
			}()
		},
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			p := req.Params
			token, ok := p.ProgressToken.(string)
			if !ok || token == "" {
				return
			}
			result := agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: ai.ContentTypeText, Text: p.Message},
				},
			}
			m.progressReg.dispatch(token, result)
		},
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			if fn := m.loadOnResourceUpdated(); fn != nil {
				fn(serverName, req.Params.URI)
			}
		},
		PromptListChangedHandler: func(_ context.Context, _ *sdk.PromptListChangedRequest) {
			firlog.Debug("MCP prompt list changed", "server", serverName)
		},
		CreateMessageHandler: m.SamplingFn,
		ElicitationHandler:   elicitHandler(m.ElicitationFn),
		// Ping the server periodically to detect dead connections.
		KeepAlive: 30 * time.Second,
	}
}

// reconnectInitialDelay, reconnectMaxDelay and reconnectErrSurfaceThreshold
// configure the auto-reconnect loop. The loop sleeps for an exponentially
// growing delay between dial attempts, with ±20% jitter to avoid thundering
// herd on simultaneous wake-ups (e.g. laptop resume).
//
// Variables (not consts) so tests can shorten them; restore via t.Cleanup.
var (
	reconnectInitialDelay        = 1 * time.Second
	reconnectMaxDelay            = 60 * time.Second
	reconnectErrSurfaceThreshold = 3
)

// startReconnectLoop spawns the auto-reconnect goroutine for an entry whose
// initial session is freshly installed. Cancels any previously running loop
// for the same entry first. Idempotent on repeated calls — but Reload uses
// reconnectCancel directly, not this function, to tear down a loop without
// starting a new one.
func (m *Manager) startReconnectLoop(name string) {
	entry := m.loadEntry(name)
	if entry == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	var prevCancel context.CancelFunc
	entry.with(func(e *serverEntry) {
		prevCancel = e.reconnectCancel
		e.reconnectCancel = cancel
	})
	if prevCancel != nil {
		prevCancel()
	}
	m.reconnectWG.Add(1)
	go func() {
		defer m.reconnectWG.Done()
		m.reconnectLoop(loopCtx, name)
	}()
}

// reconnectLoop is the per-entry auto-reconnect goroutine. It alternates
// between waiting for the current session to terminate and dialling a fresh
// one, with exponential backoff between failed dials. It exits only when
// loopCtx is cancelled (Close, Reload-stop, or config-change).
func (m *Manager) reconnectLoop(ctx context.Context, name string) {
	for ctx.Err() == nil {
		// Wait for the current session (if any) to terminate.
		var session *sdk.ClientSession
		m.withEntry(name, func(e *serverEntry) { session = e.session })
		if session != nil {
			waitErr := session.Wait()
			m.handleSessionEnd(name, session, waitErr)
		}
		if ctx.Err() != nil {
			return
		}
		// Reconnect. tryReconnect blocks (backoff + dial). It returns true
		// on success; false on either dial failure (we retry immediately)
		// or ctx cancellation (we exit).
		for ctx.Err() == nil {
			if m.tryReconnect(ctx, name) {
				break
			}
		}
	}
}

// handleSessionEnd is called when session.Wait() returns. It clears the
// entry's session/tools, replaces the ready chan so future CallTools wait
// on the next generation, and (for non-benign waitErrs) records the error.
// Resets the dial-attempt counter so reconnect backoff starts at zero.
func (m *Manager) handleSessionEnd(name string, session *sdk.ClientSession, waitErr error) {
	var (
		stillCurrent  bool
		disconnectErr error
	)
	m.withEntry(name, func(e *serverEntry) {
		if e.session != session {
			// Replaced/closed by Reload or Close — nothing to do.
			return
		}
		stillCurrent = true
		e.session = nil
		e.tools = nil
		// Replace ready chan so any CallTool that arrives next sees an open
		// chan to wait on. The old chan stays closed (it was closed when the
		// session was installed); existing waiters were already woken.
		e.ready = make(chan struct{})
		e.attempt = 0
		if waitErr != nil && !isBenignCloseErr(waitErr) {
			disconnectErr = fmt.Errorf("disconnected: %w", waitErr)
			e.err = disconnectErr
		} else {
			e.err = nil
		}
	})
	if notify := m.loadOnToolsChanged(); notify != nil {
		notify(m.allTools())
	}
	if stillCurrent {
		m.emitServerEvent(ServerEvent{Kind: ServerDisconnected, Name: name, Err: disconnectErr})
	}
}

// tryReconnect performs one reconnect attempt: sleep on backoff (interruptible
// by kick or ctx), then dial+initialize. On success installs the new session
// and returns true. On dial failure increments attempt, optionally surfaces
// the error, and returns false. On ctx cancellation returns false.
func (m *Manager) tryReconnect(ctx context.Context, name string) bool {
	var (
		cfg     ServerConfig
		kick    chan struct{}
		attempt int
	)
	m.withEntry(name, func(e *serverEntry) {
		cfg = e.config
		kick = e.kick
		attempt = e.attempt
	})

	if attempt > 0 {
		delay := reconnectBackoff(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		case <-kick:
			timer.Stop()
		}
	} else {
		// First attempt of this reconnect cycle — surface the
		// "connecting" event once per cycle (not per retry).
		m.emitServerEvent(ServerEvent{Kind: ServerConnecting, Name: name})
	}
	// Drain any pending kick so the next backoff cycle starts cleanly.
	select {
	case <-kick:
	default:
	}

	sess, tools, caps, err := m.dialAndInitialize(ctx, name, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		var surface bool
		m.withEntry(name, func(e *serverEntry) {
			e.attempt++
			if e.attempt >= reconnectErrSurfaceThreshold {
				e.err = fmt.Errorf("reconnect: %w", err)
				surface = true
			}
		})
		if surface {
			firlog.Warn("MCP reconnect failing", "server", name, "attempt", attempt+1, "err", err)
		} else {
			firlog.Debug("MCP reconnect failed", "server", name, "attempt", attempt+1, "err", err)
		}
		return false
	}

	m.installReconnectedSession(name, sess, tools, caps)
	firlog.Info("mcp reconnected", "server", name, "tools", len(tools))
	return true
}

// installReconnectedSession atomically installs a freshly dialled session,
// closes the entry's ready chan to wake waiters, clears any previous error,
// and fires onToolsChanged.
func (m *Manager) installReconnectedSession(name string, sess *sdk.ClientSession, tools []agent.AgentTool, caps *sdk.ServerCapabilities) {
	var ready chan struct{}
	m.withEntry(name, func(e *serverEntry) {
		e.session = sess
		e.tools = tools
		e.caps = caps
		e.err = nil
		e.attempt = 0
		// Defensive: ensure ready is open before we close it to signal
		// "session installed". The reconnect loop's handleSessionEnd
		// always assigns a fresh open chan, so this is normally a no-op.
		select {
		case <-e.ready:
			e.ready = make(chan struct{})
		default:
		}
		ready = e.ready
	})
	close(ready)
	if notify := m.loadOnToolsChanged(); notify != nil {
		notify(m.allTools())
	}
	m.emitServerEvent(ServerEvent{Kind: ServerReady, Name: name})
}

// reconnectBackoff returns the sleep duration before reconnect attempt N
// (1-indexed). Exponential: 1s, 2s, 4s, 8s, 16s, 32s, capped at 60s. Jitter
// is ±20% via math/rand/v2, independent per call so simultaneous wakes
// (e.g. laptop resume hitting many entries) decorrelate cleanly.
func reconnectBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := reconnectInitialDelay << (attempt - 1)
	if d <= 0 || d > reconnectMaxDelay {
		d = reconnectMaxDelay
	}
	jitter := time.Duration(int64(d) / 5)
	if jitter <= 0 {
		return d
	}
	off := time.Duration(rand.Int64N(int64(2*jitter + 1)))
	return d - jitter + off
}

// dialAndInitialize performs the full Connect → list-tools → assemble-tools
// sequence for one server. It returns the new session, the full tool list
// (including resource/prompt tools where the server advertises support), and
// the cached capabilities. It does NOT touch the entry; callers install the
// returned values atomically via installReconnectedSession (or, for the
// initial connect, via startServer's inline writes).
//
// On any failure the partially-opened session (if any) is closed before
// returning so we never leak.
func (m *Manager) dialAndInitialize(ctx context.Context, name string, cfg ServerConfig) (*sdk.ClientSession, []agent.AgentTool, *sdk.ServerCapabilities, error) {
	transport, err := m.dialFn(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create transport: %w", err)
	}
	if m.verbose {
		transport = &sdk.LoggingTransport{Transport: transport, Writer: os.Stderr}
	}
	transport = wrapTransportForChannels(transport, name, func(cm ChannelMessage) {
		if fn := m.loadOnChannelMessage(); fn != nil {
			go fn(cm)
		}
	})

	opts := m.clientOptions(name)
	client := sdk.NewClient(&sdk.Implementation{Name: "fir", Version: "dev"}, opts)

	rootURIs := cfg.Roots
	if len(rootURIs) == 0 {
		if cwd, err := os.Getwd(); err == nil {
			rootURIs = []string{(&url.URL{Scheme: "file", Path: cwd}).String()}
		}
	}
	roots := make([]*sdk.Root, 0, len(rootURIs))
	for _, uri := range rootURIs {
		roots = append(roots, &sdk.Root{URI: uri})
	}
	if len(roots) > 0 {
		client.AddRoots(roots...)
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect: %w", err)
	}

	// Best-effort logging level.
	if lerr := session.SetLoggingLevel(ctx, &sdk.SetLoggingLevelParams{
		Level: m.loggingLevel(),
	}); lerr != nil {
		firlog.Debug("MCP SetLoggingLevel not supported", "server", name, "err", lerr)
	}

	var tools []agent.AgentTool
	for tool, terr := range session.Tools(ctx, nil) {
		if terr != nil {
			_ = session.Close()
			return nil, nil, nil, fmt.Errorf("list tools: %w", terr)
		}
		tools = append(tools, AdaptTool(m.serverSession(name), name, tool, &m.progressReg))
	}

	initResult := session.InitializeResult()
	var caps *sdk.ServerCapabilities
	if initResult != nil {
		caps = initResult.Capabilities
	}
	if caps != nil && caps.Resources != nil {
		tools = append(tools,
			listResourcesTool(session, name),
			readResourceTool(session, name, m.subscribeOnce(session, name)),
		)
	}
	if caps != nil && caps.Prompts != nil {
		tools = append(tools,
			listPromptsTool(session, name),
			getPromptTool(session, name),
		)
	}
	return session, tools, caps, nil
}

// Reload performs a live reconfiguration of the Manager with newConfigs.
// Servers that were removed are stopped, new servers are started, and servers
// whose config changed are stopped and restarted. Unchanged servers keep their
// existing sessions. Returns the updated aggregate tool list.
//
// Reload is safe to call concurrently with tool executions in progress on
// unchanged servers, and is safe to call concurrently from multiple goroutines
// — concurrent Reload calls are serialised by an internal mutex.
func (m *Manager) Reload(ctx context.Context, newConfigs map[string]ServerConfig) ([]agent.AgentTool, error) {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	oldConfigs := m.configsSnapshot()

	type stopItem struct {
		name    string
		session *sdk.ClientSession
	}
	var toStop []stopItem
	toStart := make(map[string]ServerConfig)

	for name, oldCfg := range oldConfigs {
		if newCfg, exists := newConfigs[name]; !exists {
			// Server removed.
			entry := m.loadEntry(name)
			if entry != nil {
				entry.with(func(e *serverEntry) {
					if e.reconnectCancel != nil {
						e.reconnectCancel()
						e.reconnectCancel = nil
					}
					if e.session != nil {
						toStop = append(toStop, stopItem{name, e.session})
					}
				})
			}
		} else if !configsEqual(oldCfg, newCfg) {
			// Server config changed — reconnect.
			entry := m.loadEntry(name)
			if entry != nil {
				entry.with(func(e *serverEntry) {
					if e.reconnectCancel != nil {
						e.reconnectCancel()
						e.reconnectCancel = nil
					}
					if e.session != nil {
						toStop = append(toStop, stopItem{name, e.session})
					}
				})
			}
			toStart[name] = newCfg
		} else {
			// Config unchanged — but reconnect synchronously if the server
			// is currently disconnected. /reload is the user's "do it now"
			// signal. The auto-reconnect loop may already be retrying in
			// the background; we cancel it and run a fresh synchronous
			// connect so callers see the result on Reload return.
			entry := m.loadEntry(name)
			if entry != nil {
				var needRestart bool
				entry.with(func(e *serverEntry) {
					if e.connecting {
						return // initial connect still in flight; leave it
					}
					if e.session != nil && e.err == nil {
						return // already connected and healthy
					}
					if e.reconnectCancel != nil {
						e.reconnectCancel()
						e.reconnectCancel = nil
					}
					if e.session != nil {
						toStop = append(toStop, stopItem{name, e.session})
					}
					needRestart = true
				})
				if needRestart {
					toStart[name] = newCfg
				}
			}
		}
	}
	for name, cfg := range newConfigs {
		if _, exists := oldConfigs[name]; !exists {
			toStart[name] = cfg
		}
	}

	// Stop removed/changed sessions. Clear state before closing so the
	// reconnect loop's stale-session check fires correctly.
	for _, item := range toStop {
		m.withEntry(item.name, func(e *serverEntry) {
			e.session = nil
			e.tools = nil
			e.err = nil
			// Reset ready to an open chan: the entry is no longer
			// "session installed". If a reconnect loop is still running
			// it would also do this in handleSessionEnd, but cancellation
			// can race ahead of that, so do it here too.
			select {
			case <-e.ready:
				e.ready = make(chan struct{})
			default:
			}
		})
		if err := item.session.Close(); err != nil {
			firlog.Warn("MCP Reload: error closing session", "server", item.name, "err", err)
		}
	}

	// Remove entries for deleted servers, add entries for new servers,
	// update config for changed servers.
	for name := range oldConfigs {
		if _, exists := newConfigs[name]; !exists {
			m.servers.Delete(name)
		}
	}
	for name, cfg := range newConfigs {
		if _, exists := oldConfigs[name]; !exists {
			// New server.
			m.servers.Store(name, newServerEntry(cfg))
		} else {
			// Update config for existing (possibly changed) server.
			m.withEntry(name, func(e *serverEntry) {
				e.config = cfg
			})
		}
	}

	// Start new/changed servers.
	for name, cfg := range toStart {
		_, err := m.startServer(ctx, name, cfg)
		if err != nil {
			m.withEntry(name, func(e *serverEntry) {
				e.err = err
			})
			firlog.Warn("MCP Reload: failed to start server", "server", name, "err", err)
		}
	}

	return m.allTools(), nil
}

// configsEqual reports whether two ServerConfigs are functionally identical
// by comparing their JSON representations.
func configsEqual(a, b ServerConfig) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// Close closes all active MCP sessions and stops their auto-reconnect loops.
// Returns the first error encountered, but always attempts to close every
// session.
func (m *Manager) Close() error {
	firlog.Debug("mcp shutting down")
	// Signal ServerEvents consumers to stop. serverEvents itself is left
	// open: in-flight Start/reconnect goroutines may still emit during the
	// teardown below, and a closed channel would panic on send.
	m.closeOnce.Do(func() { close(m.done) })
	var sessions []*sdk.ClientSession
	var cancels []context.CancelFunc
	m.forEachServer(func(name string, e *serverEntry) bool {
		if e.reconnectCancel != nil {
			cancels = append(cancels, e.reconnectCancel)
			e.reconnectCancel = nil
		}
		if e.session != nil {
			sessions = append(sessions, e.session)
			e.session = nil
		}
		// Close any open ready chans so blocked CallTools wake up. Use
		// non-blocking detect-then-close to handle the case where the
		// loop already closed it (session was installed at Close time).
		select {
		case <-e.ready:
			// already closed
		default:
			close(e.ready)
		}
		return true
	})
	for _, cancel := range cancels {
		cancel()
	}

	// Close sessions FIRST: this unblocks any reconnect-loop goroutines
	// currently parked inside session.Wait(). Only then can we WG.Wait()
	// for them to observe ctx cancellation and exit cleanly.
	var firstErr error
	for _, session := range sessions {
		if err := session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Wait for reconnect loops to actually exit, so callers know no
	// goroutine still references the manager's state.
	m.reconnectWG.Wait()
	return firstErr
}

// CallTool programmatically calls a tool on a named MCP server.
// This is used for infrastructure concerns (e.g. typing indicators) rather
// than agent-driven tool calls. If the server is currently disconnected,
// CallTool kicks the auto-reconnect loop to skip its backoff sleep, then
// waits (bounded by ctx) for the session to come back. Returns ctx.Err() on
// cancellation or "not connected" if the entry has been removed entirely.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*sdk.CallToolResult, error) {
	session, err := m.ensureConnected(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return session.CallTool(ctx, &sdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

// serverSession returns a SessionGetter that resolves to the entry's
// currently-installed session for serverName, transparently waiting for
// auto-reconnect if the session is nil. Used by AdaptTool so a tool call
// invoked between disconnect and reconnect blocks (bounded by ctx) instead
// of failing.
func (m *Manager) serverSession(serverName string) SessionGetter {
	return func(ctx context.Context) (*sdk.ClientSession, error) {
		return m.ensureConnected(ctx, serverName)
	}
}

// ensureConnected returns the current session for serverName, or waits for
// the auto-reconnect loop to install one. If no loop is running (entry was
// torn down, or initial connect failed and was never retried), returns
// "not connected" immediately. Bounded by ctx.
//
// Concurrent ensureConnected callers all snapshot the same ready chan and
// share a single in-flight reconnect — no thundering herd.
func (m *Manager) ensureConnected(ctx context.Context, serverName string) (*sdk.ClientSession, error) {
	// Fast path.
	var session *sdk.ClientSession
	m.withEntry(serverName, func(e *serverEntry) { session = e.session })
	if session != nil {
		return session, nil
	}

	// Slow path: snapshot ready chan, kick the loop, wait.
	var (
		ready   chan struct{}
		kick    chan struct{}
		hasLoop bool
		entryOK bool
	)
	entryOK = m.withEntry(serverName, func(e *serverEntry) {
		ready = e.ready
		kick = e.kick
		hasLoop = e.reconnectCancel != nil
	})
	if !entryOK {
		return nil, fmt.Errorf("MCP server %q not configured", serverName)
	}
	if !hasLoop {
		// Initial connect failed and was never retried, or Close() ran.
		return nil, fmt.Errorf("MCP server %q not connected", serverName)
	}
	// Non-blocking kick.
	select {
	case kick <- struct{}{}:
	default:
	}
	// Wait for the loop to install a session, or ctx to expire.
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Refetch session under lock. The reconnect closed `ready` *after*
	// installing the session, so it should be non-nil — but it could have
	// dropped again immediately. Single retry then give up to avoid loops.
	m.withEntry(serverName, func(e *serverEntry) { session = e.session })
	if session == nil {
		return nil, fmt.Errorf("MCP server %q reconnect raced with disconnect", serverName)
	}
	return session, nil
}

// IsServerConnecting returns true if the server is still performing its
// initial connection/initialize handshake.
func (m *Manager) IsServerConnecting(serverName string) bool {
	var connecting bool
	m.withEntry(serverName, func(e *serverEntry) {
		connecting = e.connecting
	})
	return connecting
}

// WaitReady blocks until every server launched by Start has finished its
// initial connection attempt (successfully or with an error), or until ctx
// is cancelled. It is safe to call before Start (returns immediately) and
// from multiple goroutines. Callers that want a timeout should pass a
// context with a deadline.
func (m *Manager) WaitReady(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.startWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HasServerTools reports whether the named server exposes all of the given
// tool names. This checks the raw MCP tool names (not the prefixed agent
// tool names).
func (m *Manager) HasServerTools(serverName string, toolNames ...string) bool {
	var serverTools []agent.AgentTool
	m.withEntry(serverName, func(e *serverEntry) {
		serverTools = e.tools
	})

	// Build a set of the raw (unprefixed) tool names this server has.
	// The agent tool name is "mcp__<server>__<tool>", so strip the prefix.
	prefix := sanitizeToolName("mcp__" + serverName + "__")
	have := make(map[string]struct{}, len(serverTools))
	for _, t := range serverTools {
		name := t.Tool.Name
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			have[name[len(prefix):]] = struct{}{}
		}
	}
	for _, need := range toolNames {
		if _, ok := have[need]; !ok {
			return false
		}
	}
	return true
}

// ServerStatus reports the connection state of a single MCP server.
type ServerStatus struct {
	// Name is the key used in the Manager's config map.
	Name string
	// Connected is true when the session is currently active.
	Connected bool
	// Error is non-nil when the server failed to connect or has disconnected
	// with an error.
	Error error
}

// isBenignCloseErr reports whether a session.Wait() error is one we should
// treat as a clean shutdown rather than a user-visible disconnect error.
//
// The streamable HTTP transport sends a DELETE request to terminate the
// session in (*streamableClientConn).Close. Some MCP servers (e.g. grafana)
// simply close the TCP connection in response to DELETE without sending an
// HTTP response, which surfaces as `Delete "URL": EOF` (a *url.Error wrapping
// io.EOF / io.ErrUnexpectedEOF). The DELETE is best-effort per spec — if the
// server already considers the session over, that's not an error condition
// for us. Any error originating from the DELETE close request is benign;
// real mid-session failures arrive via Op="Get"/"Post" or non-url errors.
func isBenignCloseErr(err error) bool {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.Op != "Delete" {
		return false
	}
	// Network EOF on close is the common case (server hangs up without
	// responding). Context cancellation during shutdown is also benign.
	return errors.Is(urlErr.Err, io.EOF) ||
		errors.Is(urlErr.Err, io.ErrUnexpectedEOF) ||
		errors.Is(urlErr.Err, context.Canceled)
}

// StatusString returns a human-readable status label: "connected",
// "disconnected", or "error: <message>".
func (s ServerStatus) StatusString() string {
	if s.Error != nil {
		return "error: " + s.Error.Error()
	}
	if s.Connected {
		return "connected"
	}
	return "disconnected"
}

// Status returns a snapshot of the health of each configured server.
// The slice is ordered by server name for deterministic output.
func (m *Manager) Status() []ServerStatus {
	var out []ServerStatus
	m.forEachServer(func(name string, e *serverEntry) bool {
		out = append(out, ServerStatus{
			Name:      name,
			Connected: e.session != nil,
			Error:     e.err,
		})
		return true
	})
	// Sort by name for deterministic output.
	slices.SortFunc(out, func(a, b ServerStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// StatusFunc returns a callback that returns server status from mgr, or nil
// if mgr is nil.  Useful for passing status to components that should not
// depend on the full Manager.
func StatusFunc(mgr *Manager) func() []ServerStatus {
	if mgr == nil {
		return nil
	}
	return mgr.Status
}

// ServerDetail provides detailed information about a single MCP server,
// including its configuration, connection status, and the tools it exposes.
type ServerDetail struct {
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Config       ServerConfig `json:"config"`
	Tools        []ToolInfo   `json:"tools,omitempty"`
	HasResources bool         `json:"has_resources,omitempty"`
	HasPrompts   bool         `json:"has_prompts,omitempty"`
	Error        string       `json:"error,omitempty"`
}

// ToolInfo is a summary of a tool exposed by an MCP server.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// Details returns detailed information about each configured MCP server,
// including config, tools, and capability flags.
func (m *Manager) Details() []ServerDetail {
	var out []ServerDetail
	m.forEachServer(func(name string, e *serverEntry) bool {
		d := ServerDetail{
			Name:   name,
			Config: e.config,
		}
		if e.err != nil {
			d.Status = "error"
			d.Error = e.err.Error()
		} else if e.session != nil {
			d.Status = "connected"
		} else if e.connecting {
			d.Status = "connecting"
		} else {
			d.Status = "disconnected"
		}
		for _, t := range e.tools {
			d.Tools = append(d.Tools, ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		if e.caps != nil {
			d.HasResources = e.caps.Resources != nil
			d.HasPrompts = e.caps.Prompts != nil
		}
		out = append(out, d)
		return true
	})
	slices.SortFunc(out, func(a, b ServerDetail) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// DetailsFunc returns a callback that returns detailed server info from mgr,
// or nil if mgr is nil.
func DetailsFunc(mgr *Manager) func() []ServerDetail {
	if mgr == nil {
		return nil
	}
	return mgr.Details
}

// HasServerToolParam checks whether a server has a tool with the given name
// whose input schema includes a specific property. Used to distinguish between
// reply tools with different signatures (e.g. message_id-addressed replies
// vs chat_id-addressed replies).
func (m *Manager) HasServerToolParam(serverName, toolName, paramName string) bool {
	var serverTools []agent.AgentTool
	m.withEntry(serverName, func(e *serverEntry) {
		serverTools = e.tools
	})
	prefix := sanitizeToolName("mcp__" + serverName + "__")
	for _, t := range serverTools {
		name := t.Tool.Name
		rawName := ""
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			rawName = name[len(prefix):]
		}
		if rawName != toolName {
			continue
		}
		// Check if the tool's Parameters schema has the property.
		if params, ok := t.Tool.Parameters.(map[string]any); ok {
			if props, ok := params["properties"].(map[string]any); ok {
				if _, ok := props[paramName]; ok {
					return true
				}
			}
		}
		return false
	}
	return false
}
