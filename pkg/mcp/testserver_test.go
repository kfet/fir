// testserver_test.go — self-invoking MCP server for e2e tests.
//
// When the test binary is invoked with MCP_TEST_SERVER=1 it runs as a minimal
// MCP server over stdin/stdout instead of executing any tests. The e2e tests in
// mcp_e2e_test.go spawn the binary that way via ServerConfig{Command: os.Args[0]}.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain is the unified entry point for the mcp package test binary.
// When MCP_TEST_SERVER=1 the process acts as an MCP server; otherwise it
// runs the normal test suite.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_TEST_SERVER") == "1" {
		runTestServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runTestServer starts a minimal MCP server on stdin/stdout.
//
// MCP_TEST_SERVER_MODE controls the variant:
//
//	""                 (default) — full server: echo, add, slow tools
//	"exit_immediately" — returns without MCP setup (simulates a crash/early exit)
func runTestServer() {
	if os.Getenv("MCP_TEST_SERVER_MODE") == "exit_immediately" {
		// Exit without setting up MCP so the client sees EOF during handshake.
		return
	}

	server := sdk.NewServer(&sdk.Implementation{Name: "test-server", Version: "0"}, nil)

	// echo: returns "echo: <message>"
	server.AddTool(
		&sdk.Tool{
			Name:        "echo",
			Description: "Echo the message back",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
		},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + args.Message}},
			}, nil
		},
	)

	// add: returns the sum of a and b
	server.AddTool(
		&sdk.Tool{
			Name:        "add",
			Description: "Add two numbers",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
		},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct {
				A float64 `json:"a"`
				B float64 `json:"b"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("%g", args.A+args.B)}},
			}, nil
		},
	)

	// slow: blocks until the handler context is cancelled (used for cancellation tests)
	server.AddTool(
		&sdk.Tool{
			Name:        "slow",
			Description: "Blocks until the request context is cancelled",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	// Run blocks until stdin is closed (client disconnects).
	_ = server.Run(context.Background(), &sdk.StdioTransport{})
}
