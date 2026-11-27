package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countSetLevel installs middleware on server that counts logging/setLevel
// requests it receives.
func countSetLevel(server *sdk.Server) *atomic.Int32 {
	var n atomic.Int32
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "logging/setLevel" {
				n.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	return &n
}

func echoServer(opts *sdk.ServerOptions) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "0"}, opts)
	sdk.AddTool(s, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	return s
}

var legacy = &sdk.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}}

// A stateless server never receives setLevel; the level rides on _meta.
func TestDial_StatelessUsesMetaLogLevel(t *testing.T) {
	server := echoServer(nil)
	n := countSetLevel(server)
	var metaLevel atomic.Value
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/call" {
				if v, ok := req.GetParams().GetMeta()[sdk.MetaKeyLogLevel]; ok {
					metaLevel.Store(v)
				}
			}
			return next(ctx, method, req)
		}
	})
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1)
	assert.Zero(t, n.Load())
	_, err := tools[0].Execute(context.Background(), "id", map[string]any{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "warning", metaLevel.Load())
}

// A server without the logging capability must never receive setLevel.
func TestDial_NoSetLevelWithoutLoggingCapability(t *testing.T) {
	server := echoServer(&sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{}})
	n := countSetLevel(server)
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1)
	assert.Zero(t, n.Load())
}

// A server that declares logging gets setLevel once per connect.
func TestDial_SetLevelWithLoggingCapability(t *testing.T) {
	server := echoServer(legacy) // SDK default declares logging
	n := countSetLevel(server)
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1)
	assert.EqualValues(t, 1, n.Load())
}

// killOnSetLevel drops the connection when the client sends
// logging/setLevel, like the go-sdk streamable transport does on a bare 400.
type killOnSetLevel struct{ inner sdk.Transport }

func (k killOnSetLevel) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := k.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &killConn{Connection: c}, nil
}

type killConn struct{ sdk.Connection }

func (c *killConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if r, ok := msg.(*jsonrpc.Request); ok && r.Method == "logging/setLevel" {
		_ = c.Close()
		return errors.New("400 Bad Request")
	}
	return c.Connection.Write(ctx, msg)
}

// If setLevel breaks the connection, fir redials once without it and the
// tools still load.
func TestDial_RedialWithoutSetLevelWhenItBreaksConn(t *testing.T) {
	server := echoServer(legacy)
	dial := inMemoryDial(t, server)
	var dials atomic.Int32
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = func(name string, cfg ServerConfig) (sdk.Transport, error) {
		dials.Add(1)
		tr, err := dial(name, cfg)
		return killOnSetLevel{tr}, err
	}

	tools := startAndWait(t, mgr, context.Background())
	require.Len(t, tools, 1)
	assert.EqualValues(t, 2, dials.Load())
}

func TestAddLogLevelMeta(t *testing.T) {
	out, ok := addLogLevelMeta(json.RawMessage(`{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`), "warning")
	require.True(t, ok)
	assert.JSONEq(t, `{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/logLevel":"warning"}}`, string(out))

	for _, in := range []string{
		``, `not json`, `{"name":"x"}`, `{"_meta":"bad"}`, `{"_meta":{}}`,
		`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/logLevel":"debug"}}`,
	} {
		_, ok := addLogLevelMeta(json.RawMessage(in), "warning")
		assert.False(t, ok, in)
	}
}

func TestDeprecatedSampling(t *testing.T) {
	assert.Nil(t, deprecatedSampling("s", nil))
	var n int
	fn := deprecatedSampling("s", func(context.Context, *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error) {
		n++
		return &sdk.CreateMessageResult{}, nil
	})
	_, _ = fn(context.Background(), nil)
	_, _ = fn(context.Background(), nil)
	assert.Equal(t, 2, n)
}
