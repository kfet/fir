package acp

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
)

func runMCPCmd(t *testing.T, mgr *mcp.Manager, args string) string {
	t.Helper()
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), session: sess, cwd: t.TempDir(), mcpManager: mgr}
	if !pa.handleSlashCommand("s1", entry, "mcp", args) {
		t.Fatalf("expected /mcp %s to be handled", args)
	}
	return getLastAgentMessage(mc.getUpdates())
}

func TestCmdMCP_LoginLogout(t *testing.T) {
	stdio := mcp.NewManager(map[string]mcp.ServerConfig{"local": {Command: "true"}}, false)
	cases := []struct {
		name string
		mgr  *mcp.Manager
		args string
		want string
	}{
		{"login usage", stdio, "login", "Usage: /mcp login <server>"},
		{"login usage spaces", stdio, "login   ", "Usage: /mcp login <server>"},
		{"logout usage", stdio, "logout", "Usage: /mcp logout <server>"},
		{"login no manager", nil, "login slack", "No MCP servers configured."},
		{"logout no manager", nil, "logout slack", "No MCP servers configured."},
		{"login unknown", stdio, "login slack", `MCP login failed: MCP server "slack" not configured`},
		{"login stdio", stdio, "login local", "MCP login failed"},
		{"logout ok", stdio, "logout local", `Removed stored credentials for MCP server "local".`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runMCPCmd(t, tc.mgr, tc.args)
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want substring %q", got, tc.want)
			}
			if strings.Contains(got, "not found") {
				t.Errorf("subcommand treated as server name: %q", got)
			}
		})
	}
}
