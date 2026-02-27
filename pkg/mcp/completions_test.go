package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newCompletionSession creates a test ClientSession connected to a server that
// handles completion requests for a "greet" prompt and a file URI template.
func newCompletionSession(t *testing.T) *sdk.ClientSession {
	t.Helper()
	server := sdk.NewServer(
		&sdk.Implementation{Name: "completion-srv", Version: "0"},
		&sdk.ServerOptions{
			CompletionHandler: func(_ context.Context, req *sdk.CompleteRequest) (*sdk.CompleteResult, error) {
				ref := req.Params.Ref
				arg := req.Params.Argument
				var values []string
				switch {
				case ref.Type == "ref/prompt" && ref.Name == "greet" && arg.Name == "name":
					for _, n := range []string{"Alice", "Bob", "Charlie"} {
						if arg.Value == "" || string(n[0]) == arg.Value[:1] {
							values = append(values, n)
						}
					}
				case ref.Type == "ref/resource" && ref.URI == "file:///{path}":
					values = []string{"/tmp/a.txt", "/tmp/b.txt"}
				}
				return &sdk.CompleteResult{
					Completion: sdk.CompletionResultDetails{Values: values},
				}, nil
			},
		},
	)
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "fir-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func TestCompletePromptArg_Matches(t *testing.T) {
	session := newCompletionSession(t)
	values, err := CompletePromptArg(context.Background(), session, "greet", "name", "A")
	require.NoError(t, err)
	assert.Equal(t, []string{"Alice"}, values)
}

func TestCompletePromptArg_AllCandidates(t *testing.T) {
	session := newCompletionSession(t)
	values, err := CompletePromptArg(context.Background(), session, "greet", "name", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"Alice", "Bob", "Charlie"}, values)
}

func TestCompleteResourceURI(t *testing.T) {
	session := newCompletionSession(t)
	values, err := CompleteResourceURI(context.Background(), session, "file:///{path}", "path", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"/tmp/a.txt", "/tmp/b.txt"}, values)
}

func TestCompletePromptArg_NoCandidates(t *testing.T) {
	session := newCompletionSession(t)
	// No match for prefix "Z".
	values, err := CompletePromptArg(context.Background(), session, "greet", "name", "Z")
	require.NoError(t, err)
	assert.Empty(t, values)
}
