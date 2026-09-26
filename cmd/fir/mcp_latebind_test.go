package main

import (
	"testing"

	"github.com/kfet/fir/pkg/mcp"
)

// A manager created by /mcp reload after startup must be visible to the
// /mcp status and details callbacks wired at startup.
func TestMCPFuncsLateBind(t *testing.T) {
	var mgr *mcp.Manager
	status, details := mcpStatusFunc(&mgr), mcpDetailsFunc(&mgr)
	if status() != nil || details() != nil {
		t.Fatal("nil manager: want nil results")
	}
	mgr = mcp.NewManager(map[string]mcp.ServerConfig{"x": {Transport: "streamable", URL: "https://example.invalid/mcp"}}, false)
	if got := len(details()); got != 1 {
		t.Fatalf("details after late manager: got %d servers, want 1", got)
	}
	if got := len(status()); got != 1 {
		t.Fatalf("status after late manager: got %d servers, want 1", got)
	}
}
