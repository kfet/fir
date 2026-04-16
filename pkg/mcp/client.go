package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	// onServerReady is called (from a background goroutine) when an individual
	// MCP server finishes its initial connection attempt. The first argument is
	// the server name; the second is nil on success or the connection error.
	// May be nil.
	onServerReady atomic.Value // func(name string, err error)

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
}

// NewManager creates a new Manager for the given server configs.
// When verbose is true, MCP server log messages are forwarded at DEBUG level
// and the logging level sent to each server is "debug"; otherwise only
// warnings and above are requested.
func NewManager(configs map[string]ServerConfig, verbose bool) *Manager {
	mgr := &Manager{
		verbose: verbose,
		dialFn:  createTransport,
	}
	for k, v := range configs {
		mgr.servers.Store(k, &serverEntry{config: v})
	}
	return mgr
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

// loadOnServerReady returns the current onServerReady callback, or nil.
func (m *Manager) loadOnServerReady() func(string, error) {
	if v := m.onServerReady.Load(); v != nil {
		return v.(func(string, error))
	}
	return nil
}

// SetOnServerReady sets the callback invoked when an individual MCP server
// finishes its initial connection attempt. The callback receives the server
// name and nil on success, or a non-nil error on failure.
// Safe to call concurrently with running servers.
func (m *Manager) SetOnServerReady(fn func(name string, err error)) {
	m.onServerReady.Store(fn)
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
		go func() {
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
			if ready := m.loadOnServerReady(); ready != nil {
				ready(name, err)
			}
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

// startServer connects to a single MCP server and returns its adapted tools.
func (m *Manager) startServer(ctx context.Context, name string, cfg ServerConfig) ([]agent.AgentTool, error) {
	firlog.Debug("mcp connecting", "server", name, "command", cfg.Command)
	transport, err := m.dialFn(cfg)
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}
	// When verbose, wrap the transport to log all JSON-RPC wire messages to
	// stderr. This is useful for debugging MCP server communication.
	if m.verbose {
		transport = &sdk.LoggingTransport{Transport: transport, Writer: os.Stderr}
	}

	// Wrap the transport to intercept channel notifications before they reach
	// the SDK's method dispatcher. The callback is resolved lazily so that
	// OnChannelMessage can be set after Start() returns. The callback is
	// dispatched asynchronously to avoid blocking the SDK's read loop.
	transport = wrapTransportForChannels(transport, name, func(cm ChannelMessage) {
		if fn := m.loadOnChannelMessage(); fn != nil {
			go fn(cm)
		}
	})

	// Route MCP server log messages to the process slog logger.
	serverName := name

	opts := &sdk.ClientOptions{
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
		// Re-enumerate tools when the server's tool list changes.
		ToolListChangedHandler: func(_ context.Context, req *sdk.ToolListChangedRequest) {
			if req.Session == nil {
				return
			}
			// Re-list tools for this server in a background goroutine.
			// Use context.Background() — the notification handler's context is
			// cancelled when the handler returns, before the goroutine finishes.
			session := req.Session
			go func() {
				var updated []agent.AgentTool
				for tool, err := range session.Tools(context.Background(), nil) {
					if err != nil {
						slog.Warn("MCP re-list tools error", "server", serverName, "err", err)
						return
					}
					updated = append(updated, AdaptTool(session, serverName, tool, &m.progressReg))
				}
				// Include resource and prompt tools only when the server
				// advertises the corresponding capability. Use cached caps
				// (stored in serverEntry after Connect) to avoid a data race.
				var caps *sdk.ServerCapabilities
				m.withEntry(serverName, func(e *serverEntry) {
					caps = e.caps
				})
				if caps == nil {
					m.withEntry(serverName, func(e *serverEntry) {
						e.tools = updated
					})
					if notify := m.loadOnToolsChanged(); notify != nil {
						notify(m.allTools())
					}
					return
				}
				if caps.Resources != nil {
					updated = append(updated,
						listResourcesTool(session, serverName),
						readResourceTool(session, serverName, m.subscribeOnce(session, serverName)),
					)
				}
				if caps.Prompts != nil {
					updated = append(updated,
						listPromptsTool(session, serverName),
						getPromptTool(session, serverName),
					)
				}

				m.withEntry(serverName, func(e *serverEntry) {
					// Guard against stale updates: only overwrite tools if this
					// session is still the active session for this server. A reload
					// may have closed the session and removed it between when the
					// notification arrived and now.
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
		// Forward progress notifications to the registered update callback.
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
		// Notify caller when a subscribed resource is updated.
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			if fn := m.loadOnResourceUpdated(); fn != nil {
				fn(serverName, req.Params.URI)
			}
		},
		// Log prompt-list change notifications (our prompt tools use live queries
		// so no re-enumeration is needed).
		PromptListChangedHandler: func(_ context.Context, _ *sdk.PromptListChangedRequest) {
			slog.Debug("MCP prompt list changed", "server", serverName)
		},
		// Forward sampling/createMessage requests to the configured handler.
		CreateMessageHandler: m.SamplingFn,
		// Forward elicitation/create requests to the configured handler.
		// Fall back to DefaultElicitFn so the server always gets a proper
		// decline response rather than a JSON-RPC "not supported" error.
		ElicitationHandler: elicitHandler(m.ElicitationFn),
		// Ping the server periodically to detect dead connections.
		KeepAlive: 30 * time.Second,
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "fir", Version: "dev"}, opts)

	// Advertise filesystem roots. Use the configured roots when present;
	// fall back to the process working directory so the server always knows
	// its operating scope.
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
		return nil, fmt.Errorf("connect: %w", err)
	}
	m.withEntry(name, func(e *serverEntry) {
		e.session = session
	})

	// Request the server to send log messages at the appropriate level.
	// Best-effort: ignore errors (e.g. server may not support logging).
	if lerr := session.SetLoggingLevel(ctx, &sdk.SetLoggingLevelParams{
		Level: m.loggingLevel(),
	}); lerr != nil {
		slog.Debug("MCP SetLoggingLevel not supported", "server", name, "err", lerr)
	}

	var tools []agent.AgentTool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, AdaptTool(session, name, tool, &m.progressReg))
	}
	firlog.Info("mcp connected", "server", name, "tools", len(tools))

	// Expose MCP resources and prompts as additional tools, but only when
	// the server advertises the corresponding capability.
	// Cache server capabilities in the entry for the ToolListChangedHandler goroutine.
	initResult := session.InitializeResult()
	var caps *sdk.ServerCapabilities
	if initResult != nil {
		caps = initResult.Capabilities
	}
	m.withEntry(name, func(e *serverEntry) {
		e.caps = caps
	})

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

	m.withEntry(name, func(e *serverEntry) {
		e.tools = tools
	})

	// Detect post-startup disconnections so Status() stays accurate.
	go func() {
		waitErr := session.Wait()
		m.withEntry(name, func(e *serverEntry) {
			if e.session != session {
				// Already replaced/removed — nothing to do.
				return
			}
			e.session = nil
			e.tools = nil
			if waitErr != nil {
				e.err = fmt.Errorf("disconnected: %w", waitErr)
			}
		})
		if notify := m.loadOnToolsChanged(); notify != nil {
			notify(m.allTools())
		}
	}()

	return tools, nil
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
					if e.session != nil {
						toStop = append(toStop, stopItem{name, e.session})
					}
				})
			}
			toStart[name] = newCfg
		}
	}
	for name, cfg := range newConfigs {
		if _, exists := oldConfigs[name]; !exists {
			toStart[name] = cfg
		}
	}

	// Stop removed/changed sessions. Clear state before closing so the Wait
	// goroutine's stale-session check fires correctly.
	for _, item := range toStop {
		m.withEntry(item.name, func(e *serverEntry) {
			e.session = nil
			e.tools = nil
			e.err = nil
		})
		if err := item.session.Close(); err != nil {
			slog.Warn("MCP Reload: error closing session", "server", item.name, "err", err)
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
			m.servers.Store(name, &serverEntry{config: cfg})
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
			slog.Warn("MCP Reload: failed to start server", "server", name, "err", err)
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

// Close closes all active MCP sessions. Returns the first error encountered,
// but always attempts to close every session.
func (m *Manager) Close() error {
	firlog.Debug("mcp shutting down")
	var sessions []*sdk.ClientSession
	m.forEachServer(func(name string, e *serverEntry) bool {
		if e.session != nil {
			sessions = append(sessions, e.session)
			e.session = nil
		}
		return true
	})

	var firstErr error
	for _, session := range sessions {
		if err := session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CallTool programmatically calls a tool on a named MCP server.
// This is used for infrastructure concerns (e.g. typing indicators) rather
// than agent-driven tool calls.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*sdk.CallToolResult, error) {
	var session *sdk.ClientSession
	m.withEntry(serverName, func(e *serverEntry) {
		session = e.session
	})
	if session == nil {
		return nil, fmt.Errorf("MCP server %q not connected", serverName)
	}
	return session.CallTool(ctx, &sdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

// HasServerTools reports whether the named server exposes all of the given
// tool names. This checks the raw MCP tool names (not the prefixed agent
// tool names).
// IsServerConnecting returns true if the server is still performing its
// initial connection/initialize handshake.
func (m *Manager) IsServerConnecting(serverName string) bool {
	var connecting bool
	m.withEntry(serverName, func(e *serverEntry) {
		connecting = e.connecting
	})
	return connecting
}

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
// reply tools with different signatures (e.g. Poe reply with "message_id"
// vs Telegram reply with "chat_id").
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
