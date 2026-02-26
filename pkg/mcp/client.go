package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/kfet/fir/pkg/agent"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Manager owns the lifecycle of all MCP client sessions for one fir session.
type Manager struct {
	configs  map[string]ServerConfig
	sessions map[string]*sdk.ClientSession

	// dialFn creates a Transport for the given server config.
	// Defaults to commandTransport. Replaced in tests to inject in-memory transports.
	dialFn func(cfg ServerConfig) (sdk.Transport, error)
}

// NewManager creates a new Manager for the given server configs.
func NewManager(configs map[string]ServerConfig) *Manager {
	return &Manager{
		configs:  configs,
		sessions: make(map[string]*sdk.ClientSession),
		dialFn:   commandTransport,
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
			_ = m.Close()
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		tools = append(tools, sessionTools...)
	}
	return tools, nil
}

// startServer connects to a single MCP server and returns its adapted tools.
func (m *Manager) startServer(ctx context.Context, name string, cfg ServerConfig) ([]agent.AgentTool, error) {
	transport, err := m.dialFn(cfg)
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "fir", Version: "dev"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	m.sessions[name] = session

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	tools := make([]agent.AgentTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		tools = append(tools, AdaptTool(session, name, tool))
	}
	return tools, nil
}

// Close closes all active MCP sessions. Returns the first error encountered,
// but always attempts to close every session.
func (m *Manager) Close() error {
	var firstErr error
	for name, session := range m.sessions {
		if err := session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.sessions, name)
	}
	return firstErr
}
