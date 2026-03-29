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

// Manager owns the lifecycle of all MCP client sessions for one fir session.
type Manager struct {
	configs  map[string]ServerConfig
	sessions map[string]*sdk.ClientSession
	verbose  bool

	mu           sync.Mutex
	reloadMu     sync.Mutex                     // serialises concurrent Reload calls
	tools        map[string][]agent.AgentTool   // per-server tools, guarded by mu
	serverErrors map[string]error               // per-server connection errors, guarded by mu
	subscribed   map[string]map[string]struct{} // per-server subscribed resource URIs, guarded by mu

	// onToolsChanged is called (from a background goroutine) whenever any
	// server's tool list changes. The argument is the new complete tool list
	// across all servers. May be nil. Set via SetOnToolsChanged.
	onToolsChanged func([]agent.AgentTool)

	// onResourceUpdated is called when a subscribed resource is updated on a
	// server. serverName is the Manager key; uri is the resource that changed.
	// May be nil. Set via SetOnResourceUpdated.
	onResourceUpdated func(serverName, uri string)

	// onChannelMessage is called when a channel-capable MCP server sends a
	// notifications/claude/channel notification. May be nil. Set via SetOnChannelMessage.
	onChannelMessage func(ChannelMessage)

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
	return &Manager{
		configs:      configs,
		sessions:     make(map[string]*sdk.ClientSession),
		verbose:      verbose,
		tools:        make(map[string][]agent.AgentTool),
		serverErrors: make(map[string]error),
		subscribed:   make(map[string]map[string]struct{}),
		dialFn:       createTransport,
	}
}

// SetOnToolsChanged sets the callback invoked when any server's tool list
// changes. Safe to call concurrently with running servers.
func (m *Manager) SetOnToolsChanged(fn func([]agent.AgentTool)) {
	m.mu.Lock()
	m.onToolsChanged = fn
	m.mu.Unlock()
}

// SetOnResourceUpdated sets the callback invoked when a subscribed resource
// is updated. Safe to call concurrently with running servers.
func (m *Manager) SetOnResourceUpdated(fn func(serverName, uri string)) {
	m.mu.Lock()
	m.onResourceUpdated = fn
	m.mu.Unlock()
}

// SetOnChannelMessage sets the callback invoked when a channel-capable MCP
// server sends a notification. Safe to call concurrently with running servers.
func (m *Manager) SetOnChannelMessage(fn func(ChannelMessage)) {
	m.mu.Lock()
	m.onChannelMessage = fn
	m.mu.Unlock()
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
// in serverErrors and logged but do not prevent other servers from starting.
func (m *Manager) Start(ctx context.Context) {
	firlog.Info("mcp starting", "servers", len(m.configs))
	for name, cfg := range m.configs {
		go func(name string, cfg ServerConfig) {
			_, err := m.startServer(ctx, name, cfg)
			if err != nil {
				firlog.Warn("mcp connection failed", "server", name, "err", err)
				m.mu.Lock()
				m.serverErrors[name] = err
				// Clean up any orphaned session that startServer may have
				// stored before failing (e.g. Connect succeeded but Tools
				// listing failed).
				if sess, ok := m.sessions[name]; ok {
					delete(m.sessions, name)
					delete(m.tools, name)
					delete(m.subscribed, name)
					m.mu.Unlock()
					_ = sess.Close()
				} else {
					m.mu.Unlock()
				}
			}
			// Notify with the updated aggregate tool list (even on error, so
			// the callback sees tools from other servers that succeeded).
			m.mu.Lock()
			all := m.allTools()
			notify := m.onToolsChanged
			m.mu.Unlock()
			if notify != nil {
				notify(all)
			}
		}(name, cfg)
	}
}

// loggingLevel returns the MCP logging level to request from servers.
func (m *Manager) loggingLevel() sdk.LoggingLevel {
	if m.verbose {
		return "debug"
	}
	return "warning"
}

// allTools returns a flat snapshot of all tools across all servers. Caller
// must hold m.mu.
func (m *Manager) allTools() []agent.AgentTool {
	var out []agent.AgentTool
	for _, ts := range m.tools {
		out = append(out, ts...)
	}
	return out
}

// subscribeOnce returns a subscribeFunc that subscribes to a resource URI at
// most once per server. It uses the Manager's subscribed map and mutex.
func (m *Manager) subscribeOnce(session *sdk.ClientSession, serverName string) subscribeFunc {
	return func(uri string) {
		m.mu.Lock()
		subs, ok := m.subscribed[serverName]
		if !ok {
			subs = make(map[string]struct{})
			m.subscribed[serverName] = subs
		}
		_, already := subs[uri]
		if !already {
			subs[uri] = struct{}{}
		}
		m.mu.Unlock()
		if !already {
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
		m.mu.Lock()
		fn := m.onChannelMessage
		m.mu.Unlock()
		if fn != nil {
			go fn(cm)
		}
	})

	// Route MCP server log messages to the process slog logger.
	serverName := name

	// serverCaps is set after Connect() returns and read by the
	// ToolListChangedHandler goroutine. Protected by capsMu.
	var capsMu sync.Mutex
	var serverCaps *sdk.ServerCapabilities

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
				// (set after Connect) to avoid a data race with Connect().
				capsMu.Lock()
				caps := serverCaps
				capsMu.Unlock()
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

				m.mu.Lock()
				// Guard against stale updates: only overwrite m.tools if this
				// session is still the active session for this server. A reload
				// may have closed the session and removed it from m.sessions
				// between when the notification arrived and now.
				current, stillActive := m.sessions[serverName]
				if !stillActive || current != session {
					m.mu.Unlock()
					return
				}
				m.tools[serverName] = updated
				all := m.allTools()
				notify := m.onToolsChanged
				m.mu.Unlock()
				if notify != nil {
					notify(all)
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
			m.mu.Lock()
			fn := m.onResourceUpdated
			m.mu.Unlock()
			if fn == nil {
				return
			}
			fn(serverName, req.Params.URI)
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
	m.mu.Lock()
	m.sessions[name] = session
	m.mu.Unlock()

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
	// Cache server capabilities for the ToolListChangedHandler goroutine.
	initResult := session.InitializeResult()
	capsMu.Lock()
	if initResult != nil {
		serverCaps = initResult.Capabilities
	}
	capsMu.Unlock()

	caps := serverCaps
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

	m.mu.Lock()
	m.tools[name] = tools
	m.mu.Unlock()

	// Detect post-startup disconnections so Status() stays accurate.
	// session.Wait() blocks until the underlying connection is closed (by
	// either side). When it returns, if this session is still the active
	// session for this server we clear it from m.sessions and record the
	// error so callers see Connected:false. If the session was already
	// replaced or removed (by Reload or Close) the stale-session check exits
	// early without clobbering the new state.
	go func() {
		waitErr := session.Wait()
		m.mu.Lock()
		current, ok := m.sessions[name]
		if !ok || current != session {
			// Already replaced/removed — nothing to do.
			m.mu.Unlock()
			return
		}
		delete(m.sessions, name)
		delete(m.tools, name)
		delete(m.subscribed, name)
		if waitErr != nil {
			m.serverErrors[name] = fmt.Errorf("disconnected: %w", waitErr)
		}
		notify := m.onToolsChanged
		all := m.allTools()
		m.mu.Unlock()
		if notify != nil {
			notify(all)
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
	// Determine which servers to stop (removed or changed) and which to start
	// (new or changed). We compare configs by JSON serialisation to avoid a
	// custom equality function.
	m.mu.Lock()
	oldConfigs := m.configs
	m.mu.Unlock()

	toStop := make(map[string]*sdk.ClientSession) // sessions to close
	toStart := make(map[string]ServerConfig)      // configs to connect

	for name, oldCfg := range oldConfigs {
		if newCfg, exists := newConfigs[name]; !exists {
			// Server removed.
			m.mu.Lock()
			if sess, ok := m.sessions[name]; ok {
				toStop[name] = sess
			}
			m.mu.Unlock()
		} else if !configsEqual(oldCfg, newCfg) {
			// Server config changed — reconnect.
			m.mu.Lock()
			if sess, ok := m.sessions[name]; ok {
				toStop[name] = sess
			}
			m.mu.Unlock()
			toStart[name] = newCfg
		}
	}
	for name, cfg := range newConfigs {
		if _, exists := oldConfigs[name]; !exists {
			toStart[name] = cfg
		}
	}

	// Stop removed/changed sessions (outside the lock — Close may block).
	for name, sess := range toStop {
		// Remove from m.sessions before closing so the Wait goroutine's
		// stale-session check (current != session) fires correctly and does
		// not overwrite the state that Reload is about to establish.
		m.mu.Lock()
		delete(m.sessions, name)
		delete(m.tools, name)
		delete(m.serverErrors, name)
		delete(m.subscribed, name)
		m.mu.Unlock()
		if err := sess.Close(); err != nil {
			slog.Warn("MCP Reload: error closing session", "server", name, "err", err)
		}
	}

	// Update the config map.
	m.mu.Lock()
	m.configs = newConfigs
	m.mu.Unlock()

	// Start new/changed servers.
	for name, cfg := range toStart {
		_, err := m.startServer(ctx, name, cfg)
		if err != nil {
			m.mu.Lock()
			m.serverErrors[name] = err
			m.mu.Unlock()
			slog.Warn("MCP Reload: failed to start server", "server", name, "err", err)
		}
	}

	m.mu.Lock()
	all := m.allTools()
	m.mu.Unlock()
	return all, nil
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
	firlog.Debug("mcp shutting down", "servers", len(m.sessions))
	m.mu.Lock()
	sessions := make(map[string]*sdk.ClientSession, len(m.sessions))
	for k, v := range m.sessions {
		sessions[k] = v
		delete(m.sessions, k)
	}
	m.mu.Unlock()

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
	m.mu.Lock()
	session, ok := m.sessions[serverName]
	m.mu.Unlock()
	if !ok {
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
func (m *Manager) HasServerTools(serverName string, toolNames ...string) bool {
	m.mu.Lock()
	serverTools := m.tools[serverName]
	m.mu.Unlock()

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
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]ServerStatus, 0, len(m.configs))
	for name := range m.configs {
		_, connected := m.sessions[name]
		out = append(out, ServerStatus{
			Name:      name,
			Connected: connected,
			Error:     m.serverErrors[name],
		})
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []ServerDetail
	for name, cfg := range m.configs {
		d := ServerDetail{
			Name:   name,
			Config: cfg,
		}
		sess := m.sessions[name]
		if connErr, ok := m.serverErrors[name]; ok && connErr != nil {
			d.Status = "error"
			d.Error = connErr.Error()
		} else if sess != nil {
			d.Status = "connected"
		} else {
			d.Status = "connecting"
		}
		if tools, ok := m.tools[name]; ok {
			for _, t := range tools {
				d.Tools = append(d.Tools, ToolInfo{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				})
			}
		}
		if sess != nil {
			caps := sess.InitializeResult().Capabilities
			if caps != nil {
				d.HasResources = caps.Resources != nil
				d.HasPrompts = caps.Prompts != nil
			}
		}
		out = append(out, d)
	}
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
