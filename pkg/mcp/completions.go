package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CompletePromptArg returns completion candidates for the given argument of a
// named MCP prompt. prefix is the current partial value typed by the user.
// Returns the server's completion values, or an error if the request fails
// (including when the server does not support the completions capability).
func CompletePromptArg(ctx context.Context, session *sdk.ClientSession, promptName, argName, prefix string) ([]string, error) {
	result, err := session.Complete(ctx, &sdk.CompleteParams{
		Ref:      &sdk.CompleteReference{Type: "ref/prompt", Name: promptName},
		Argument: sdk.CompleteParamsArgument{Name: argName, Value: prefix},
	})
	if err != nil {
		return nil, err
	}
	return result.Completion.Values, nil
}

// CompleteResourceURI returns completion candidates for the given argument of
// a MCP resource URI template. prefix is the current partial value typed by
// the user. Returns the server's completion values, or an error if the
// request fails (including when the server does not support the completions
// capability).
func CompleteResourceURI(ctx context.Context, session *sdk.ClientSession, uriTemplate, argName, prefix string) ([]string, error) {
	result, err := session.Complete(ctx, &sdk.CompleteParams{
		Ref:      &sdk.CompleteReference{Type: "ref/resource", URI: uriTemplate},
		Argument: sdk.CompleteParamsArgument{Name: argName, Value: prefix},
	})
	if err != nil {
		return nil, err
	}
	return result.Completion.Values, nil
}
