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
	session    *sdk.ClientSession // nil while connecting or after disconnect
	tools      []agent.AgentTool  // tools exposed by this server
	err        error              // last connection/disconnect error
	connecting bool               // true while initial connect is in progress
	subscribed sync.Map           // uri (string) → struct{}
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

type Manager struct {
	servers  sync.Map // string → *serverEntry
	verbose  bool
	reloadMu sync.Mutex // serialises concurrent Reload calls

	// OnToolsChanged is called (from a background goroutine) whenever any
	// server's tool list changes. The argument is the new complete tool list
	// across all servers. May be nil.
	OnToolsChanged atomic.Value // func([]agent.AgentTool)

	// OnResourceUpdated is called when a subscribed resource is updated on a
	// server. serverName is the Manager key; uri is the resource that changed.
	// May be nil.
	OnResourceUpdated atomic.Value // func(serverName, uri string)

	// OnChannelMessage is called when a channel-capable MCP server sends a
	// notifications/claude/channel notification. May be nil.
	OnChannelMessage atomic.Value // func(ChannelMessage)

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

// loadOnToolsChanged returns the current OnToolsChanged callback, or nil.
func (m *Manager) loadOnToolsChanged() func([]agent.AgentTool) {
	if v := m.OnToolsChanged.Load(); v != nil {
		return v.(func([]agent.AgentTool))
	}
	return nil
}

// loadOnChannelMessage returns the current OnChannelMessage callback, or nil.
func (m *Manager) loadOnChannelMessage() func(ChannelMessage) {
	if v := m.OnChannelMessage.Load(); v != nil {
		return v.(func(ChannelMessage))
	}
	return nil
}

// loadOnResourceUpdated returns the current OnResourceUpdated callback, or nil.
func (m *Manager) loadOnResourceUpdated() func(string, string) {
	if v := m.OnResourceUpdated.Load(); v != nil {
		return v.(func(string, string))
	}
	return nil
}

// configsLen returns the number of configured servers.
func (m *Manager) configsLen() int {
	n := 0
	m.forEachServer(func(_ string, _ *serverEntry) bool { n++; return true })
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
				caps := session.InitializeResult().Capabilities
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

				// Guard against stale updates: only overwrite tools if this
				// session is still the active session for this server.
				active := false
				m.withEntry(serverName, func(e *serverEntry) {
					if e.session != session {
						return
					}
					active = true
					e.tools = updated
				})
				if !active {
					return
				}
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
		ElicitationHandler: elicitHandler(m.ElicitationFn),
		// Ping the server periodically to detect dead connections.
		KeepAlive: 30 * time.Second,
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "fir", Version: "dev"}, opts)

	// Advertise filesystem roots.
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

	entry := m.loadEntry(name)
	if entry == nil {
		_ = session.Close()
		return nil, fmt.Errorf("server %q removed during connect", name)
	}
	m.withEntry(name, func(e *serverEntry) {
		e.session = session
	})

	// Request the server to send log messages at the appropriate level.
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

	caps := session.InitializeResult().Capabilities
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
		active := false
		m.withEntry(name, func(e *serverEntry) {
			if e.session != session {
				return
			}
			active = true
			e.session = nil
			e.tools = nil
			if waitErr != nil {
				e.err = fmt.Errorf("disconnected: %w", waitErr)
			}
		})
		if !active {
			return
		}
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
		name string
		sess *sdk.ClientSession
	}
	var toStop []stopItem
	toStart := make(map[string]ServerConfig)

	for name, oldCfg := range oldConfigs {
		if newCfg, exists := newConfigs[name]; !exists {
			// Server removed.
			var sess *sdk.ClientSession
			m.withEntry(name, func(e *serverEntry) { sess = e.session })
			if sess != nil {
				toStop = append(toStop, stopItem{name, sess})
			}
		} else if !configsEqual(oldCfg, newCfg) {
			// Server config changed — reconnect.
			var sess *sdk.ClientSession
			m.withEntry(name, func(e *serverEntry) { sess = e.session })
			if sess != nil {
				toStop = append(toStop, stopItem{name, sess})
			}
			toStart[name] = newCfg
		}
	}
	for name, cfg := range newConfigs {
		if _, exists := oldConfigs[name]; !exists {
			toStart[name] = cfg
		}
	}

	// Stop removed/changed servers.
	for _, item := range toStop {
		m.servers.Delete(item.name)
		if err := item.sess.Close(); err != nil {
			slog.Warn("MCP Reload: error closing session", "server", item.name, "err", err)
		}
	}

	for name, cfg := range newConfigs {
		if _, starting := toStart[name]; starting {
			m.servers.Store(name, &serverEntry{config: cfg})
		} else {
			m.withEntry(name, func(e *serverEntry) { e.config = cfg })
		}
	}

	// Remove entries for servers no longer in newConfigs.
	m.servers.Range(func(key, _ any) bool {
		if _, exists := newConfigs[key.(string)]; !exists {
			m.servers.Delete(key)
		}
		return true
	})

	// Start new/changed servers.
	for name, cfg := range toStart {
		_, err := m.startServer(ctx, name, cfg)
		if err != nil {
			m.withEntry(name, func(e *serverEntry) { e.err = err })
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
	var sessions []*sdk.ClientSession
	m.forEachServer(func(_ string, e *serverEntry) bool {
		if e.session != nil {
			sessions = append(sessions, e.session)
			e.session = nil
			e.tools = nil
		}
		return true
	})
	firlog.Debug("mcp shutting down", "servers", len(sessions))

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
	if !m.withEntry(serverName, func(e *serverEntry) { session = e.session }) || session == nil {
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
	var serverTools []agent.AgentTool
	if !m.withEntry(serverName, func(e *serverEntry) { serverTools = e.tools }) {
		return false
	}

	prefix := sanitizeToolName("mcp__" + serverName + "__")
	have := make(map[string]struct{}, len(serverTools))
	for _, t := range serverTools {
		tname := t.Tool.Name
		if len(tname) > len(prefix) && tname[:len(prefix)] == prefix {
			have[tname[len(prefix):]] = struct{}{}
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
	// Status is a human-readable label: "connected", "connecting",
	// "disconnected", or "error: <message>".
	Status string
}

// Status returns a snapshot of the health of each configured server.
// The slice is ordered by server name for deterministic output.
func (m *Manager) Status() []ServerStatus {
	var out []ServerStatus
	m.forEachServer(func(name string, e *serverEntry) bool {
		var status string
		switch {
		case e.err != nil:
			status = "error: " + e.err.Error()
		case e.session != nil:
			status = "connected"
		case e.connecting:
			status = "connecting"
		default:
			status = "disconnected"
		}
		out = append(out, ServerStatus{Name: name, Status: status})
		return true
	})
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

// WatchAndReload watches path for changes to the MCP config file and
// incrementally applies the diff:
//   - New servers are started.
//   - Removed servers are stopped.
//   - Changed servers are stopped then restarted.
//   - Unchanged servers are left alone.
//
// ctx is used when connecting to newly added or changed servers.
// Returns a stop function that terminates the file watcher.
func (m *Manager) WatchAndReload(ctx context.Context, path string) (stop func(), err error) {
	return WatchConfig(path, func(newCfg *ConfigFile) {
		tools, reloadErr := m.Reload(ctx, newCfg.MCPServers)
		if reloadErr != nil {
			slog.Warn("mcp: config reload failed", "path", path, "err", reloadErr)
			return
		}
		if notify := m.loadOnToolsChanged(); notify != nil {
			notify(tools)
		}
	})
}
