package mcp

import (
	"context"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultElicitFn returns an elicitation handler that always declines without
// prompting the user. This is the safe default for non-interactive (headless)
// sessions where no UI is available to present the request.
//
// Assign Manager.ElicitationFn to an interactive implementation to allow MCP
// servers to ask the user questions through fir's UI.
func DefaultElicitFn(_ context.Context, _ *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	return &sdk.ElicitResult{Action: "decline"}, nil
}

// ElicitFormResult is a convenience wrapper that returns an "accept" result
// with the given key-value content, suitable for responding to form-mode
// elicitations.
func ElicitFormResult(content map[string]any) *sdk.ElicitResult {
	return &sdk.ElicitResult{
		Action:  "accept",
		Content: content,
	}
}

// ElicitMessage extracts the human-readable message from an ElicitRequest,
// falling back to a generic description if the message is empty.
func ElicitMessage(req *sdk.ElicitRequest) string {
	if req.Params.Message != "" {
		return req.Params.Message
	}
	switch req.Params.Mode {
	case "url":
		return fmt.Sprintf("Open URL: %s", req.Params.URL)
	default:
		return "An MCP server is requesting input."
	}
}

// elicitHandler returns fn if non-nil, otherwise DefaultElicitFn.
// This ensures the SDK always has a handler wired up, so MCP servers receive
// a proper "decline" response rather than a JSON-RPC "not supported" error.
func elicitHandler(fn func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)) func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	if fn != nil {
		return fn
	}
	return DefaultElicitFn
}
