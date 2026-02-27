package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/pkg/agent"
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

	mgr := NewManager(map[string]ServerConfig{"myserver": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 3) // 1 MCP tool + 2 resource tools

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

	mgr := NewManager(map[string]ServerConfig{"calc": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 3) // 1 MCP tool + 2 resource tools

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
	mgr := NewManager(configs, false)

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
	assert.Len(t, tools, 6) // 1 MCP tool + 2 resource tools per server
}

func TestManager_Close(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
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
	mgr := NewManager(nil, false)
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

	mgr := NewManager(map[string]ServerConfig{"paged": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	// Both tools must be present despite the page size of 1.
	require.Len(t, tools, 4) // 2 MCP tools + 2 resource tools

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
	mgr := NewManager(map[string]ServerConfig{"s": cfg}, false)
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
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
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

// TestManager_ToolListChanged verifies that when the MCP server adds a tool
// after the session is established, the OnToolsChanged callback is invoked
// with the updated aggregate tool list.
func TestManager_ToolListChanged(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	// Server starts with no tools.

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	changed := make(chan []agent.AgentTool, 1)
	mgr.OnToolsChanged = func(tools []agent.AgentTool) {
		changed <- tools
	}

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	// Server has no MCP tools yet but resource tools are always present.
	assert.Len(t, tools, 2, "expect only list_resources + read_resource before any tool is added")

	// Adding a tool after connection sends a tool-list-changed notification.
	server.AddTool(
		&sdk.Tool{Name: "new_tool", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "new"}}}, nil
		},
	)

	select {
	case newTools := <-changed:
		require.Len(t, newTools, 3) // 1 MCP tool + 2 resource tools
		assert.Equal(t, "mcp__srv__new_tool", newTools[0].Name)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tool list change notification")
	}
}

// TestManager_ProgressNotification verifies that progress notifications sent
// by the MCP server during a tool call are forwarded to the onUpdate callback.
func TestManager_ProgressNotification(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "slow", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			if token := req.Params.GetProgressToken(); token != nil {
				_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{
					ProgressToken: token,
					Message:       "halfway there",
					Progress:      50,
					Total:         100,
				})
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "done"}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools, err := mgr.Start(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 3) // 1 MCP tool + 2 resource tools

	updates := make(chan string, 10)
	onUpdate := func(result agent.AgentToolResult) {
		if len(result.Content) > 0 {
			updates <- result.Content[0].Text
		}
	}

	result, err := tools[0].Execute(context.Background(), "call-id-1", nil, onUpdate)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "done", result.Content[0].Text)

	// The progress notification is sent by the server before returning the tool
	// result, so it is already buffered in updates by the time Execute returns.
	// Collect with a short timeout in case of any scheduler delay.
	var got string
	select {
	case got = <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for progress notification")
	}
	assert.Equal(t, "halfway there", got)
}

// chanHandler is a slog.Handler that sends each Record to a buffered channel.
type chanHandler struct {
	ch chan slog.Record
}

func (h *chanHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *chanHandler) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.ch <- r:
	default:
	}
	return nil
}
func (h *chanHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *chanHandler) WithGroup(string) slog.Handler      { return h }

// TestManager_LoggingHandler verifies that log messages emitted by an MCP
// server are routed through slog at the correct level. We temporarily replace
// the default slog logger with a chanHandler, start the manager with
// verbose=true, then send a warning log from the server session and assert
// that it arrives in slog with slog.LevelWarn.
func TestManager_LoggingHandler(t *testing.T) {
	// Capture slog records via a buffered channel.
	ch := make(chan slog.Record, 10)
	origLogger := slog.Default()
	slog.SetDefault(slog.New(&chanHandler{ch: ch}))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	server := sdk.NewServer(&sdk.Implementation{Name: "log-test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	// verbose=true → client requests "debug" log level from server.
	mgr := NewManager(map[string]ServerConfig{"s": {}}, true)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	_, err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	// Get the server-side session and send a warning log notification to the client.
	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss, "server must have an active session")

	require.NoError(t, ss.Log(ctx, &sdk.LoggingMessageParams{
		Level:  "warning",
		Logger: "test-logger",
		Data:   "hello from server",
	}))

	// Wait for the log notification to arrive in slog.
	var rec slog.Record
	select {
	case rec = <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for MCP server log message in slog")
	}

	assert.Equal(t, slog.LevelWarn, rec.Level, "MCP 'warning' must map to slog.LevelWarn")
	assert.Equal(t, "MCP server log", rec.Message)
}

// TestManager_LoggingLevelVerbose verifies that loggingLevel() returns "debug"
// when verbose=true and "warning" otherwise.
func TestManager_LoggingLevelVerbose(t *testing.T) {
	mgr := NewManager(nil, true)
	assert.Equal(t, sdk.LoggingLevel("debug"), mgr.loggingLevel())

	mgr2 := NewManager(nil, false)
	assert.Equal(t, sdk.LoggingLevel("warning"), mgr2.loggingLevel())
}

// TestCreateTransport_UnknownType verifies that an unsupported Transport value
// returns an error rather than silently falling back to stdio.
func TestCreateTransport_UnknownType(t *testing.T) {
	_, err := createTransport(ServerConfig{Transport: "ftp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport")
	assert.Contains(t, err.Error(), "ftp")
}

// TestCreateTransport_Stdio verifies that the default transport (no Transport
// field set) produces a *sdk.CommandTransport with the correct command.
func TestCreateTransport_Stdio(t *testing.T) {
	cfg := ServerConfig{
		Command: "true",
		Env:     map[string]string{"CUSTOM": "value"},
	}
	tr, err := createTransport(cfg)
	require.NoError(t, err)
	ct, ok := tr.(*sdk.CommandTransport)
	require.True(t, ok, "expected *sdk.CommandTransport for stdio transport")
	assert.Equal(t, "true", ct.Command.Args[0])
}

// TestCreateTransport_SSE verifies that transport="sse" produces an
// *sdk.SSEClientTransport pointing at the configured URL.
func TestCreateTransport_SSE(t *testing.T) {
	cfg := ServerConfig{Transport: "sse", URL: "http://example.com/sse"}
	tr, err := createTransport(cfg)
	require.NoError(t, err)
	st, ok := tr.(*sdk.SSEClientTransport)
	require.True(t, ok, "expected *sdk.SSEClientTransport for sse transport")
	assert.Equal(t, "http://example.com/sse", st.Endpoint)
}

// TestCreateTransport_SSE_MissingURL verifies that transport="sse" without a
// URL returns an error.
func TestCreateTransport_SSE_MissingURL(t *testing.T) {
	_, err := createTransport(ServerConfig{Transport: "sse"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

// TestCreateTransport_Streamable verifies that transport="streamable" produces
// an *sdk.StreamableClientTransport pointing at the configured URL.
func TestCreateTransport_Streamable(t *testing.T) {
	cfg := ServerConfig{Transport: "streamable", URL: "http://example.com/mcp"}
	tr, err := createTransport(cfg)
	require.NoError(t, err)
	st, ok := tr.(*sdk.StreamableClientTransport)
	require.True(t, ok, "expected *sdk.StreamableClientTransport for streamable transport")
	assert.Equal(t, "http://example.com/mcp", st.Endpoint)
}

// TestCreateTransport_Streamable_MissingURL verifies that transport="streamable"
// without a URL returns an error.
func TestCreateTransport_Streamable_MissingURL(t *testing.T) {
	_, err := createTransport(ServerConfig{Transport: "streamable"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

// TestCreateTransport_UnknownTransport verifies that an unrecognised transport
// name returns an error rather than silently falling through to stdio.
func TestCreateTransport_UnknownTransport(t *testing.T) {
	_, err := createTransport(ServerConfig{Transport: "ftp", Command: "true"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport")
	assert.Contains(t, err.Error(), "ftp")
}

// TestManager_StreamableTransport_Integration runs a real streamable HTTP MCP
// server in-process using httptest.NewServer and verifies that Manager can
// connect to it, list tools, and call a tool over the streamable transport.
func TestManager_StreamableTransport_Integration(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "streamable-test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{
			Name:        "ping",
			Description: "Return pong",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "pong"}},
			}, nil
		},
	)

	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	cfg := ServerConfig{Transport: "streamable", URL: httpSrv.URL}
	mgr := NewManager(map[string]ServerConfig{"http-srv": cfg}, false)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tools, err := mgr.Start(ctx)
	require.NoError(t, err)
	defer mgr.Close()

	require.Len(t, tools, 3) // 1 MCP tool + 2 resource tools
	assert.Equal(t, "mcp__http-srv__ping", tools[0].Name)

	result, err := tools[0].Execute(ctx, "call-1", nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "pong", result.Content[0].Text)
}
