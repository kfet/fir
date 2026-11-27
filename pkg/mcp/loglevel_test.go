package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fir never sends logging/setLevel, on either protocol, even to a server
// that declares logging (SEP-2577 deprecates it).
func TestDial_NeverSendsSetLevel(t *testing.T) {
	for name, opts := range map[string]*sdk.ServerOptions{
		"stateless": nil,
		"legacy":    {SupportedProtocolVersions: []string{"2025-11-25"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "0"}, opts)
			sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
				return &sdk.CallToolResult{}, nil, nil
			})
			var n atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "logging/setLevel" {
						n.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
			mgr.dialFn = inMemoryDial(t, server)
			tools := startAndWait(t, mgr, context.Background())
			require.Len(t, tools, 1)
			_, err := tools[0].Execute(context.Background(), "id", map[string]any{}, nil)
			require.NoError(t, err)
			assert.Zero(t, n.Load())
		})
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
