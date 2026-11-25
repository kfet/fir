package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// A connected server's initialize instructions reach the system prompt, and
// survive a reap and rehydrate.
func TestMCPInstructions_ReachSystemPrompt(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	srv := echoMCPServer("stitch")
	srv.Stdio.Env = append(srv.Stdio.Env, acpsdk.EnvVariable{Name: "MCP_TEST_INSTRUCTIONS", Value: "Bookmark decisions."})
	const sid = "acp-sid-instructions"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd, mcpServers: []acpsdk.McpServer{srv}})
	waitMCPTools(t, entry, "mcp__stitch__echo")
	if p := entry.session.GetSystemPrompt(); !strings.Contains(p, "## stitch\nBookmark decisions.") {
		t.Fatalf("instructions missing from system prompt:\n%s", p)
	}

	reapNow(t, pa, sid, entry)
	back := rehydrate(t, pa, sid)
	if p := back.session.GetSystemPrompt(); !strings.Contains(p, "## stitch\nBookmark decisions.") {
		t.Fatalf("instructions lost after rehydrate:\n%s", p)
	}
}
