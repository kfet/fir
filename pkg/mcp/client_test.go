package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/agent"
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

// startAndWait is a test helper that wires OnToolsChanged, calls Start(),
// and blocks until all servers have reported their tools (or timeout).
func startAndWait(t *testing.T, mgr *Manager, ctx context.Context) []agent.AgentTool {
	t.Helper()
	nConfigs := mgr.configsLen()
	if nConfigs == 0 {
		mgr.Start(ctx)
		return nil
	}
	// Keep a non-blocking onToolsChanged installed for tests that observe the
	// callback path. The send MUST NOT block: Reload fires onToolsChanged while
	// holding reloadMu, so a blocking send on a full buffer would wedge every
	// concurrent Reload (e.g. TestManager_Reload_Concurrent_ConfigChange) into a
	// deadlock that only the global test timeout could break.
	ch := make(chan []agent.AgentTool, 10)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		select {
		case ch <- tools:
		default:
		}
	})
	mgr.Start(ctx)
	// Deterministically wait for every configured server's initial connect to
	// finish (startWG, via WaitReady) instead of counting onToolsChanged
	// callbacks against a fixed drain window. The old count+drain approach
	// raced: a second server's aggregate could arrive after the window and
	// return a partial tool list ("3 vs 6") on a loaded CI runner. startWG.Done
	// fires only after startServer installs the session and tools, so once
	// WaitReady returns, allTools() is the complete, stable aggregate — no
	// timing window, no flake.
	waitCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		// Failsafe only (not a synchronisation mechanism): bound an otherwise
		// unbounded wait so a genuinely broken connect fails fast instead of
		// hanging to the global test timeout.
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	if err := mgr.WaitReady(waitCtx); err != nil {
		t.Fatalf("waiting for MCP servers to start: %v", err)
	}
	return mgr.allTools()
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
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "Hello, " + args.Name}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"myserver": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1) // 1 MCP tool (no resources/prompts capability)

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

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1) // 1 MCP tool (no resources/prompts capability)

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

	var dialCalls atomic.Int32
	serverMap := map[string]*sdk.Server{"srvA": serverA, "srvB": serverB}
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		// We can't easily map config→server by name here, so round-robin
		// between the two. The actual tool names identify which server answered.
		n := dialCalls.Add(1)
		var s *sdk.Server
		if n == 1 {
			s = serverMap["srvA"]
		} else {
			s = serverMap["srvB"]
		}
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = s.Run(context.Background(), serverTransport) }()
		return clientTransport, nil
	}

	tools := startAndWait(t, mgr, context.Background())
	assert.Len(t, tools, 2) // 1 MCP tool per server (no resources/prompts capability)
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

	startAndWait(t, mgr, context.Background())
	assert.True(t, mgr.hasSession("s"))

	require.NoError(t, mgr.Close())
	assert.False(t, mgr.hasSession("s"))

	// Double-close is safe.
	require.NoError(t, mgr.Close())
}

func TestManager_EmptyConfigs(t *testing.T) {
	mgr := NewManager(nil, false)
	tools := startAndWait(t, mgr, context.Background())
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

	tools := startAndWait(t, mgr, context.Background())
	// Both tools must be present despite the page size of 1.
	require.Len(t, tools, 2) // 2 MCP tools (no resources/prompts capability)

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
	startAndWait(t, mgr, ctx)
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
	startAndWait(t, mgr, ctx)
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

	changed := make(chan []agent.AgentTool, 10)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		changed <- tools
	})

	mgr.Start(context.Background())

	// Wait for initial connection (server has no tools yet).
	select {
	case tools := <-changed:
		assert.Empty(t, tools, "expect no tools before any are added (no resources/prompts capability)")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for initial connection")
	}

	// Adding a tool after connection sends a tool-list-changed notification.
	server.AddTool(
		&sdk.Tool{Name: "new_tool", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "new"}}}, nil
		},
	)

	select {
	case newTools := <-changed:
		require.Len(t, newTools, 1) // 1 MCP tool (no resources/prompts capability)
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

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1) // 1 MCP tool (no resources/prompts capability)

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
	startAndWait(t, mgr, ctx)
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

	// Wait for the server-log notification to arrive in slog. Other unrelated
	// records (e.g. a transient "MCP re-list tools error" from startup races
	// observed under CI load) may arrive on the same channel — skip past them.
	deadline := time.After(3 * time.Second)
	var rec slog.Record
	found := false
	for !found {
		select {
		case rec = <-ch:
			if rec.Message == "MCP server log" {
				found = true
			}
		case <-deadline:
			t.Fatal("timeout waiting for MCP server log message in slog")
		}
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

	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	require.Len(t, tools, 1) // 1 MCP tool (no resources/prompts capability)
	assert.Equal(t, "mcp__http-srv__ping", tools[0].Name)

	result, err := tools[0].Execute(ctx, "call-1", nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "pong", result.Content[0].Text)
}

func TestManager_Status_Connected(t *testing.T) {
	makeServer := func() *sdk.Server {
		s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
		s.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
			})
		return s
	}
	mgr := NewManager(map[string]ServerConfig{
		"alpha": {},
		"beta":  {},
	}, false)
	mgr.dialFn = inMemoryDial(t, makeServer())

	ctx := context.Background()
	startAndWait(t, mgr, ctx)
	defer mgr.Close()

	statuses := mgr.Status()
	require.Len(t, statuses, 2)
	// Sorted by name.
	assert.Equal(t, "alpha", statuses[0].Name)
	assert.True(t, statuses[0].Connected)
	assert.NoError(t, statuses[0].Error)
	assert.Equal(t, "beta", statuses[1].Name)
	assert.True(t, statuses[1].Connected)
}

func TestManager_Status_AfterClose(t *testing.T) {
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	s.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, s)

	ctx := context.Background()
	startAndWait(t, mgr, ctx)

	// Close the manager — sessions map cleared.
	require.NoError(t, mgr.Close())

	statuses := mgr.Status()
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Connected)
}

func TestManager_Status_ConnectError(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{"bad": {}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		return nil, errors.New("no such binary")
	}

	ch := make(chan []agent.AgentTool, 1)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		ch <- tools
	})
	mgr.Start(context.Background())

	// Wait for the async failure to be recorded.
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for failed server notification")
	}

	statuses := mgr.Status()
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Connected)
	assert.Error(t, statuses[0].Error)
}

// srvEvent mirrors a ServerEvent for test assertions.
type srvEvent struct {
	name string
	err  error
}

// drainServerEvents forwards a Manager's lifecycle events into per-kind
// channels until the Manager is closed. Buffers are generous so the forwarder
// never blocks. Start it before mgr.Start to observe the initial events;
// because ServerEvents is itself buffered, attaching later still works.
func drainServerEvents(mgr *Manager) (connecting chan string, ready chan srvEvent, disconnected chan srvEvent) {
	connecting = make(chan string, 16)
	ready = make(chan srvEvent, 16)
	disconnected = make(chan srvEvent, 16)
	go func() {
		for {
			select {
			case <-mgr.Done():
				return
			case ev := <-mgr.ServerEvents():
				switch ev.Kind {
				case ServerConnecting:
					connecting <- ev.Name
				case ServerReady:
					ready <- srvEvent{ev.Name, ev.Err}
				case ServerDisconnected:
					disconnected <- srvEvent{ev.Name, ev.Err}
				}
			}
		}
	}()
	return connecting, ready, disconnected
}

func TestManager_OnServerReady_Success(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	_, readyCh, _ := drainServerEvents(mgr)
	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	select {
	case ev := <-readyCh:
		assert.Equal(t, "srv", ev.name)
		assert.NoError(t, ev.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ServerReady event")
	}
}

func TestManager_OnServerReady_Error(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{"bad": {}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		return nil, errors.New("dial failed")
	}

	_, readyCh, _ := drainServerEvents(mgr)

	toolsCh := make(chan []agent.AgentTool, 1)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		toolsCh <- tools
	})
	mgr.Start(context.Background())

	// Wait for the failure to propagate.
	select {
	case <-toolsCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tools notification")
	}

	select {
	case ev := <-readyCh:
		assert.Equal(t, "bad", ev.name)
		assert.Error(t, ev.err)
		assert.Contains(t, ev.err.Error(), "dial failed")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ServerReady event")
	}
}

// TestManager_OnServerConnecting_InitialAndReconnect verifies that a
// ServerConnecting event is emitted once at the initial dial and once at the
// start of a reconnect cycle following a server-initiated disconnect.
func TestManager_OnServerConnecting_InitialAndReconnect(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	connectingCh, _, _ := drainServerEvents(mgr)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	// Initial connect must fire connecting once.
	select {
	case name := <-connectingCh:
		assert.Equal(t, "srv", name)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for initial onServerConnecting")
	}

	// Force a server-initiated disconnect; reconnect cycle should fire
	// connecting again.
	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss, "server must have an active session")
	require.NoError(t, ss.Close())

	select {
	case name := <-connectingCh:
		assert.Equal(t, "srv", name)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for reconnect onServerConnecting")
	}
}

// TestManager_ServerEvents_BufferedUntilConsumerAttaches verifies that a
// consumer attached AFTER Start has begun dialing still observes the in-flight
// "connecting" event, because ServerEvents is buffered. This mirrors the
// interactive TUI path (cmd/fir/app.go), where the consumer is attached only
// after session.Setup has already started the MCP servers.
func TestManager_ServerEvents_BufferedUntilConsumerAttaches(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	realDial := inMemoryDial(t, server)
	dialEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		// By the time dialFn runs, startServer has already emitted the
		// "connecting" event into the buffered channel. Signal that, then
		// hold the connection open so the server stays connecting.
		select {
		case dialEntered <- struct{}{}:
		default:
		}
		<-release
		return realDial(cfg)
	}

	// Start with NO consumer attached yet.
	mgr.Start(context.Background())
	defer mgr.Close()

	// Wait until startServer has emitted the connecting event and the server
	// is mid-connect.
	<-dialEntered
	require.True(t, mgr.IsServerConnecting("srv"))

	// Attach the consumer late — the buffered event must still be delivered.
	connectingCh, _, _ := drainServerEvents(mgr)

	select {
	case name := <-connectingCh:
		assert.Equal(t, "srv", name)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for buffered ServerConnecting event")
	}

	close(release) // allow the dial to complete cleanly
}

// TestManager_OnServerDisconnected fires the disconnected callback when
// the server-side session is closed unexpectedly.
func TestManager_OnServerDisconnected(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	// Succeed once, then fail forever — handleSessionEnd must surface a
	// non-benign waitErr (the SDK reports the abrupt server-close as such).
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		if dialCount.Add(1) == 1 {
			return realDial(cfg)
		}
		return nil, errors.New("dial fails after first connect")
	}

	_, _, discCh := drainServerEvents(mgr)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss, "server must have an active session")
	require.NoError(t, ss.Close())

	select {
	case ev := <-discCh:
		assert.Equal(t, "srv", ev.name)
		// err may be nil for a clean server-side EOF; the event itself
		// is what matters for surfacing the disconnect to the user.
		if ev.err != nil {
			assert.Contains(t, ev.err.Error(), "disconnected")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for ServerDisconnected event")
	}
}

// TestManager_OnServerDisconnected_NotFiredOnClose verifies that a clean
// Manager.Close() does not surface a disconnected event — those are
// reserved for unexpected session terminations.
func TestManager_OnServerDisconnected_NotFiredOnClose(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	startAndWait(t, mgr, context.Background())
	require.NoError(t, mgr.Close())

	// Drain any buffered events directly (not via the Done-gated helper, so a
	// spurious disconnect emitted at close time would still be observed) and
	// assert none is a disconnect.
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case ev := <-mgr.ServerEvents():
			if ev.Kind == ServerDisconnected {
				t.Fatal("ServerDisconnected must not fire on clean Close")
			}
		case <-deadline:
			return
		}
	}
}

// TestManager_OnServerReady_FiresOnReconnect verifies that a ServerReady event
// fires a second time after a disconnect/reconnect cycle, matching the
// connecting/connected lifecycle the user sees in the UI.
func TestManager_OnServerReady_FiresOnReconnect(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	// Speed up reconnect so the test isn't bottlenecked on backoff.
	shortenReconnectDelays(t)

	_, readyCh, _ := drainServerEvents(mgr)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	// Initial ready.
	select {
	case ev := <-readyCh:
		assert.Equal(t, "srv", ev.name)
		assert.NoError(t, ev.err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for initial ServerReady")
	}

	// Force disconnect; reconnect should fire ready again.
	closeServerSession(t, server)

	select {
	case ev := <-readyCh:
		assert.Equal(t, "srv", ev.name)
		assert.NoError(t, ev.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnect onServerReady")
	}
}

// TestManager_Status_AfterServerDisconnect verifies that when a server
// disconnects after the initial connection is established, Status() updates to
// reflect Connected:false and a non-nil Error.
func TestManager_Status_AfterServerDisconnect(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	// Counting dial that succeeds once then fails permanently. The first
	// (initial) connect is the only one that yields a working session, so
	// auto-reconnect will keep failing and Status() will stably report
	// disconnected — that's what we're asserting here.
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		if dialCount.Add(1) == 1 {
			return realDial(cfg)
		}
		return nil, errors.New("dial fails after first connect")
	}

	ctx := context.Background()
	startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Confirm connected initially.
	statuses := mgr.Status()
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Connected)
	assert.NoError(t, statuses[0].Error)

	// Close the server-side session to simulate a server-initiated disconnect.
	var ss *sdk.ServerSession
	for s := range server.Sessions() {
		ss = s
		break
	}
	require.NotNil(t, ss, "server must have an active session")
	require.NoError(t, ss.Close())

	// Auto-reconnect's dialFn is rigged to fail, so the session stays down.
	// Status should stably reflect that.
	require.Eventually(t, func() bool {
		st := mgr.Status()
		return len(st) == 1 && !st[0].Connected
	}, 3*time.Second, 25*time.Millisecond, "Status() must show disconnected when reconnect cannot succeed")

	statuses = mgr.Status()
	assert.False(t, statuses[0].Connected)
}

func TestManager_VerboseLoggingTransport(t *testing.T) {
	// When verbose=true the transport is wrapped with a LoggingTransport.
	// Verify that verbose mode doesn't break normal operation.
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "hi", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "hey"}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"verbose-srv": {}}, true /* verbose */)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	require.Len(t, tools, 1)
	result, err := tools[0].Execute(context.Background(), "c1", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "hey", result.Content[0].Text)
}

// TestManager_WaitReady_Success verifies that WaitReady returns once the
// initial connection attempt for every configured server has finished.
func TestManager_WaitReady_Success(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"a": {}, "b": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	defer mgr.Close()

	mgr.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, mgr.WaitReady(ctx))

	// Once WaitReady returns, no server should be marked connecting.
	assert.False(t, mgr.IsServerConnecting("a"))
	assert.False(t, mgr.IsServerConnecting("b"))
}

// TestManager_WaitReady_ReturnsOnFailedConnect verifies that WaitReady also
// returns when the initial connect fails (not just on success).
func TestManager_WaitReady_ReturnsOnFailedConnect(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{"bad": {}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		return nil, errors.New("nope")
	}

	mgr.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, mgr.WaitReady(ctx))
}

// TestManager_WaitReady_NoConfigs returns immediately when there are no
// configured servers.
func TestManager_WaitReady_NoConfigs(t *testing.T) {
	mgr := NewManager(nil, false)
	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
}

// TestManager_WaitReady_ContextCanceled returns ctx.Err() if the context is
// cancelled before the initial connects finish.
func TestManager_WaitReady_ContextCanceled(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	mgr := NewManager(map[string]ServerConfig{"slow": {}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		<-block
		return nil, errors.New("never")
	}

	mgr.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := mgr.WaitReady(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestIsBenignCloseErr verifies that EOF and context-cancellation on the
// DELETE close request are treated as benign (server hung up without
// responding to our session-terminate DELETE), while other errors are not.
func TestIsBenignCloseErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain EOF", io.EOF, false},
		{"DELETE EOF", &url.Error{Op: "Delete", URL: "http://x/mcp", Err: io.EOF}, true},
		{"DELETE unexpected EOF", &url.Error{Op: "Delete", URL: "http://x/mcp", Err: io.ErrUnexpectedEOF}, true},
		{"DELETE ctx canceled", &url.Error{Op: "Delete", URL: "http://x/mcp", Err: context.Canceled}, true},
		{"DELETE other err", &url.Error{Op: "Delete", URL: "http://x/mcp", Err: errors.New("connection refused")}, false},
		{"GET EOF", &url.Error{Op: "Get", URL: "http://x/mcp", Err: io.EOF}, false},
		{"POST EOF", &url.Error{Op: "Post", URL: "http://x/mcp", Err: io.EOF}, false},
		{"wrapped DELETE EOF", fmt.Errorf("disconnected: %w", &url.Error{Op: "Delete", URL: "http://x/mcp", Err: io.EOF}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isBenignCloseErr(tc.err))
		})
	}
}

func TestMcpShutdownGrace(t *testing.T) {
	t.Setenv("FIR_MCP_SHUTDOWN_GRACE", "")
	if got := mcpShutdownGrace(); got != 500*time.Millisecond {
		t.Errorf("default = %v, want 500ms", got)
	}
	t.Setenv("FIR_MCP_SHUTDOWN_GRACE", "2s")
	if got := mcpShutdownGrace(); got != 2*time.Second {
		t.Errorf("override = %v, want 2s", got)
	}
	for _, bad := range []string{"nonsense", "0", "-1s"} {
		t.Setenv("FIR_MCP_SHUTDOWN_GRACE", bad)
		if got := mcpShutdownGrace(); got != 500*time.Millisecond {
			t.Errorf("bad %q = %v, want default 500ms", bad, got)
		}
	}
}

func TestCommandTransport_SetsTerminateDuration(t *testing.T) {
	t.Setenv("FIR_MCP_SHUTDOWN_GRACE", "750ms")
	tr, err := commandTransport(ServerConfig{Command: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatalf("commandTransport: %v", err)
	}
	ct, ok := tr.(*sdk.CommandTransport)
	if !ok {
		t.Fatalf("expected *sdk.CommandTransport, got %T", tr)
	}
	if ct.TerminateDuration != 750*time.Millisecond {
		t.Errorf("TerminateDuration = %v, want 750ms", ct.TerminateDuration)
	}
}
