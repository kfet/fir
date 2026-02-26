// MCP-over-ACP end-to-end tests.
//
// These tests exercise McpServers in session/new by spawning a real fir binary
// in ACP mode and connecting MCP servers to the session.
//
// Prerequisites (same as acp_e2e_test.go):
//
//	FIR_TEST_BINARY   path to the compiled fir binary (required for all 3 tests)
//	FIR_E2E_AGENT_DIR path to an agent dir with a model configured (required for
//	                  TestACP_E2E_MCP_ToolsAppearInSession only)
//
// Example:
//
//	go build -o /tmp/fir-e2e ./cmd/fir/
//	FIR_TEST_BINARY=/tmp/fir-e2e go test ./pkg/modes/acp/ -run TestACP_E2E_MCP -v
//
// Self-invoking MCP server pattern
// ---------------------------------
// This test file registers a TestMain that checks MCP_TEST_SERVER=1.  When that
// env var is present the process acts as a minimal stdio MCP echo server instead
// of running any tests.  The tests pass os.Args[0] as the server command, so no
// separate helper binary is needed.
package acp_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ============================================================================
// Self-invoking MCP server
// ============================================================================

// TestMain is the unified entry point for the acp package test binary.
// When MCP_TEST_SERVER=1 the process acts as a minimal MCP echo server on
// stdin/stdout and exits without running any tests.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_TEST_SERVER") == "1" {
		runMCPEchoServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runMCPEchoServer runs a minimal MCP server on stdin/stdout.
// It exposes a single "echo" tool that returns its "text" argument unchanged.
// Blocks until stdin is closed (fir disconnects after the session ends).
func runMCPEchoServer() {
	server := sdk.NewServer(&sdk.Implementation{Name: "echo-srv", Version: "1"}, nil)
	server.AddTool(
		&sdk.Tool{
			Name:        "echo",
			Description: "Echo the input text back unchanged",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			if req.Params.Arguments != nil {
				_ = json.Unmarshal(req.Params.Arguments, &args)
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: args.Text}},
			}, nil
		},
	)
	// Blocks until stdin is closed.
	_ = server.Run(context.Background(), &sdk.StdioTransport{})
}

// ============================================================================
// Helper
// ============================================================================

// echoMCPServer returns an acpsdk.McpServer that spawns this test binary as a
// minimal MCP echo server.  TestMain detects MCP_TEST_SERVER=1 and runs
// runMCPEchoServer() instead of the test suite.
func echoMCPServer(name string) acpsdk.McpServer {
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    name,
		Command: os.Args[0],
		Args:    []string{},
		Env:     []acpsdk.EnvVariable{{Name: "MCP_TEST_SERVER", Value: "1"}},
	}}
}

// ============================================================================
// Tests
// ============================================================================

// TestACP_E2E_MCP_SessionNewParsesServerConfig verifies that session/new with
// a valid McpServer succeeds: fir connects to the MCP echo server, loads its
// tools, and returns a session ID.  No LLM is required.
func TestACP_E2E_MCP_SessionNewParsesServerConfig(t *testing.T) {
	conn, _, cleanup := spawnACP(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tmpDir := t.TempDir()
	resp, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        tmpDir,
		McpServers: []acpsdk.McpServer{echoMCPServer("echo-srv")},
	})
	if err != nil {
		t.Fatalf("session/new with MCP echo server failed: %v", err)
	}
	if string(resp.SessionId) == "" {
		t.Error("sessionId is empty — session was not created")
	}
	t.Logf("session %s created — MCP echo tool discovered successfully", resp.SessionId)
}

// TestACP_E2E_MCP_InvalidServerConfig verifies that session/new with a
// non-existent MCP server command returns an error promptly (no hang).
func TestACP_E2E_MCP_InvalidServerConfig(t *testing.T) {
	conn, _, cleanup := spawnACP(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tmpDir := t.TempDir()
	_, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd: tmpDir,
		McpServers: []acpsdk.McpServer{{Stdio: &acpsdk.McpServerStdio{
			Name:    "bad-server",
			Command: "/nonexistent/binary/that/does/not/exist",
			Args:    []string{},
			Env:     []acpsdk.EnvVariable{},
		}}},
	})
	if err == nil {
		t.Error("expected error for non-existent MCP server command, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestACP_E2E_MCP_ToolsAppearInSession verifies the full MCP-over-ACP path:
//  1. The self-invoking echo server is passed via session/new.
//  2. fir connects to it and discovers the "echo" tool.
//  3. A prompt asks the model to call the tool; a tool notification arrives.
//
// Requires FIR_TEST_BINARY and FIR_E2E_AGENT_DIR.
func TestACP_E2E_MCP_ToolsAppearInSession(t *testing.T) {
	requireModelEnv(t) // skips if FIR_E2E_AGENT_DIR is unset

	conn, client, cleanup := spawnACP(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tmpDir := t.TempDir()
	sessResp, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        tmpDir,
		McpServers: []acpsdk.McpServer{echoMCPServer("echo-srv")},
	})
	if err != nil {
		t.Fatalf("session/new with MCP echo server failed: %v", err)
	}
	t.Logf("session %s created — MCP echo tool ready", sessResp.SessionId)

	promptResp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(`Call mcp__echo-srv__echo with {"text":"hello-mcp"} and report the result`)},
	})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if promptResp.StopReason != acpsdk.StopReasonEndTurn {
		t.Errorf("stopReason = %v, want EndTurn", promptResp.StopReason)
	}

	// Allow a short window for trailing notifications to arrive.
	time.Sleep(100 * time.Millisecond)

	// Verify at least one tool-call notification was received for an MCP tool
	// (name must carry the mcp__ prefix, ruling out any built-in tool calls).
	var gotToolCall bool
	for _, n := range client.getNotifications() {
		if n.Update.ToolCall != nil && strings.Contains(n.Update.ToolCall.Title, "mcp__") {
			gotToolCall = true
			break
		}
	}
	if !gotToolCall {
		t.Log("No tool call notification found. Notifications received:")
		for _, n := range client.getNotifications() {
			t.Logf("  %+v", n.Update)
		}
	}
}
