package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// inMemoryDial returns a dialFn that connects to a pre-built MCP server via
// an in-memory transport. The server is started in a background goroutine.
func inMemoryDial(t *testing.T, server *sdk.Server) func(ServerConfig) (sdk.Transport, error) {
	t.Helper()
	return func(_ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() {
			_ = server.Run(context.Background(), serverTransport)
		}()
		return clientTransport, nil
	}
}

func TestManager_StartAndListTools(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{
			Name:        "greet",
			Description: "Greet someone",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct{ Name string `json:"name"` }
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "Hello, " + args.Name}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"myserver": {}})
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)

	assert.Equal(t, "mcp__myserver__greet", tools[0].Name)
	assert.Equal(t, "Greet someone", tools[0].Description)
}

func TestManager_ToolCallable(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{
			Name:        "add",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`),
		},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct {
				A float64 `json:"a"`
				B float64 `json:"b"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			result := args.A + args.B
			text, _ := json.Marshal(result)
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: string(text)}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"calc": {}})
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)

	result, err := tools[0].Execute(context.Background(), "id1", map[string]any{"a": 3.0, "b": 4.0}, nil)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "7", result.Content[0].Text)
}

func TestManager_MultipleServers(t *testing.T) {
	makeServer := func(toolName string) *sdk.Server {
		s := sdk.NewServer(&sdk.Implementation{Name: toolName, Version: "0"}, nil)
		s.AddTool(
			&sdk.Tool{Name: toolName, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: toolName}}}, nil
			},
		)
		return s
	}

	serverA := makeServer("toolA")
	serverB := makeServer("toolB")

	configs := map[string]ServerConfig{"srvA": {}, "srvB": {}}
	mgr := NewManager(configs)

	dialCalls := 0
	serverMap := map[string]*sdk.Server{"srvA": serverA, "srvB": serverB}
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		// We can't easily map config→server by name here, so round-robin
		// between the two. The actual tool names identify which server answered.
		dialCalls++
		var s *sdk.Server
		if dialCalls == 1 {
			s = serverMap["srvA"]
		} else {
			s = serverMap["srvB"]
		}
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = s.Run(context.Background(), serverTransport) }()
		return clientTransport, nil
	}

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 2)
}

func TestManager_Close(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"s": {}})
	mgr.dialFn = inMemoryDial(t, server)

	_, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.Len(t, mgr.sessions, 1)

	require.NoError(t, mgr.Close())
	assert.Empty(t, mgr.sessions)

	// Double-close is safe.
	require.NoError(t, mgr.Close())
}

func TestManager_EmptyConfigs(t *testing.T) {
	mgr := NewManager(nil)
	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tools)
	require.NoError(t, mgr.Close())
}

// TestManager_PaginatedToolList verifies that Manager correctly collects all
// tools even when the MCP server returns them across multiple pages. We
// configure the server with PageSize=1 and register 2 tools so that two
// tools/list requests are required to enumerate them all.
func TestManager_PaginatedToolList(t *testing.T) {
	server := sdk.NewServer(
		&sdk.Implementation{Name: "paged", Version: "0"},
		&sdk.ServerOptions{PageSize: 1},
	)
	for _, name := range []string{"tool_a", "tool_b"} {
		server.AddTool(
			&sdk.Tool{
				Name:        name,
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{
					Content: []sdk.Content{&sdk.TextContent{Text: name}},
				}, nil
			},
		)
	}

	mgr := NewManager(map[string]ServerConfig{"paged": {}})
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	// Both tools must be present despite the page size of 1.
	require.Len(t, tools, 2)

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	assert.True(t, names["mcp__paged__tool_a"], "tool_a missing")
	assert.True(t, names["mcp__paged__tool_b"], "tool_b missing")
}

// TestManager_RootsAdvertised verifies that filesystem roots configured on a
// ServerConfig are advertised to the MCP server. After Manager.Start() the
// server-side session can call ListRoots back to the client and must receive
// the configured URI.
func TestManager_RootsAdvertised(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "roots-test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	const wantURI = "file:///testroot"
	cfg := ServerConfig{Roots: []string{wantURI}}
	mgr := NewManager(map[string]ServerConfig{"s": cfg})
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	_, err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Retrieve the active server-side session and ask the client for its roots.
	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss, "server must have an active session")

	result, err := ss.ListRoots(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result.Roots, 1, "expected exactly one root")
	assert.Equal(t, wantURI, result.Roots[0].URI)
}

// TestManager_RootsDefaultToCWD verifies that when no roots are configured,
// the process working directory is used as the default root.
func TestManager_RootsDefaultToCWD(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "roots-default", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	// No roots in config → Manager should default to CWD.
	mgr := NewManager(map[string]ServerConfig{"s": {}})
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	_, err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss)

	result, err := ss.ListRoots(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result.Roots, 1, "expected exactly one default root")
	assert.True(t, strings.HasPrefix(result.Roots[0].URI, "file:///"), "root URI must be a file:// URI")
}

// TestCommandTransport_EnvInheritsParent verifies that commandTransport merges
// cfg.Env on top of the current process environment (os.Environ), not replacing
// it. If os.Environ() were dropped, PATH would be missing and real subprocesses
// would fail to find their binaries.
func TestCommandTransport_EnvInheritsParent(t *testing.T) {
	cfg := ServerConfig{
		Command: "true", // any real binary on PATH
		Env:     map[string]string{"MCP_TEST_CUSTOM_VAR": "hello"},
	}
	transport, err := commandTransport(cfg)
	require.NoError(t, err)

	ct, ok := transport.(*sdk.CommandTransport)
	require.True(t, ok, "expected *sdk.CommandTransport")

	// cmd.Env must contain both the parent's PATH and the custom override.
	envMap := make(map[string]string, len(ct.Command.Env))
	for _, e := range ct.Command.Env {
		k, v, _ := strings.Cut(e, "=")
		envMap[k] = v
	}
	assert.Equal(t, "hello", envMap["MCP_TEST_CUSTOM_VAR"], "custom env var must be set")
	// os.Environ() typically contains PATH; verify parent env was inherited.
	assert.Contains(t, envMap, "PATH", "subprocess env must inherit PATH from parent")
	// The parent env should be present (os.Environ is always non-empty).
	assert.True(t, len(ct.Command.Env) > 1, "env must have more than just the custom var")
}

// TestCommandTransport_EmptyCommand returns an error for an empty command.
func TestCommandTransport_EmptyCommand(t *testing.T) {
	_, err := commandTransport(ServerConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}
