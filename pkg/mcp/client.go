package mcp

import (
	"context"
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
	tools        map[string][]agent.AgentTool // per-server tools, guarded by mu
	serverErrors map[string]error             // per-server connection errors, guarded by mu

	// OnToolsChanged is called (from a background goroutine) whenever any
	// server's tool list changes. The argument is the new complete tool list
	// across all servers. May be nil.
	OnToolsChanged func([]agent.AgentTool)

	// OnResourceUpdated is called when a subscribed resource is updated on a
	// server. serverName is the Manager key; uri is the resource that changed.
	// May be nil.
	OnResourceUpdated func(serverName, uri string)

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
		dialFn:       createTransport,
	}
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

// Start connects to all configured MCP servers and returns the aggregate tool list.
// If any server fails to start, already-started sessions are closed before returning
// the error.
func (m *Manager) Start(ctx context.Context) ([]agent.AgentTool, error) {
	var tools []agent.AgentTool
	for name, cfg := range m.configs {
		sessionTools, err := m.startServer(ctx, name, cfg)
		if err != nil {
			m.mu.Lock()
			m.serverErrors[name] = err
			m.mu.Unlock()
			_ = m.Close()
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		tools = append(tools, sessionTools...)
	}
	return tools, nil
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

// startServer connects to a single MCP server and returns its adapted tools.
func (m *Manager) startServer(ctx context.Context, name string, cfg ServerConfig) ([]agent.AgentTool, error) {
	transport, err := m.dialFn(cfg)
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}

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
			if m.OnToolsChanged == nil || req.Session == nil {
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
				// Always include the resource and prompt tools for this server.
				updated = append(updated,
					listResourcesTool(session, serverName),
					readResourceTool(session, serverName),
					listPromptsTool(session, serverName),
					getPromptTool(session, serverName),
				)
				m.mu.Lock()
				m.tools[serverName] = updated
				all := m.allTools()
				m.mu.Unlock()
				m.OnToolsChanged(all)
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
			if m.OnResourceUpdated == nil {
				return
			}
			m.OnResourceUpdated(serverName, req.Params.URI)
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
	// Expose MCP resources and prompts as additional tools.
	tools = append(tools,
		listResourcesTool(session, name),
		readResourceTool(session, name),
		listPromptsTool(session, name),
		getPromptTool(session, name),
	)

	// Subscribe to each resource for push update notifications. Best-effort:
	// servers that don't support subscriptions return an error which we ignore.
	for res, err := range session.Resources(ctx, nil) {
		if err != nil {
			break
		}
		_ = session.Subscribe(ctx, &sdk.SubscribeParams{URI: res.URI})
	}

	m.mu.Lock()
	m.tools[name] = tools
	m.mu.Unlock()
	return tools, nil
}

// Close closes all active MCP sessions. Returns the first error encountered,
// but always attempts to close every session.
func (m *Manager) Close() error {
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
