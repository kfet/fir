package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
)

// advertisedMCP returns the sorted mcp__* tool names in the agent's tool set.
func advertisedMCP(s *AgentSession) []string {
	var out []string
	s.Agent.UpdateTools(func(ts *agent.ToolSet) {
		for _, n := range ts.Names() {
			if strings.HasPrefix(n, "mcp__") {
				out = append(out, n)
			}
		}
	})
	sort.Strings(out)
	return out
}

func savedTool(name string) []ai.Tool {
	return []ai.Tool{{Name: name, Parameters: map[string]any{"type": "object"}}}
}

func writeMCPConfig(t *testing.T, cwd, cfg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cwd, ".fir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".fir", "mcp.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The saved tool list follows the configured server set at once, whatever
// the connection state: on rehydrate and on every /reload a removed or
// renamed server's tools are gone, a configured but unconnected server keeps
// its saved tools, and a new server has none until it connects. Servers
// here never connect (/bin/false), so only saved state can supply tools.
func TestMCPTools_FollowConfiguredServers(t *testing.T) {
	t.Setenv("FIR_AGENT_DIR", t.TempDir())
	sess, cwd, _ := newSessionWithAuthPath(t)
	sess.UpdateSessionState(func(st *SessionState) {
		st.Runtime.McpTools = map[string][]ai.Tool{
			"kept":    savedTool("mcp__kept__a"),
			"gone":    savedTool("mcp__gone__b"),
			"oldname": savedTool("mcp__oldname__c"),
			"client":  savedTool("mcp__client__d"),
		}
		st.Runtime.McpServers = map[string]mcp.ServerConfig{"client": {Command: "/bin/false"}}
	})
	broken := mcp.ServerConfig{Command: "/bin/false"}

	// Rehydrate: "gone" removed, "oldname" renamed to "newname", "added" new.
	mgr := StartMCPManager(context.Background(), sess, mergeMCP(map[string]mcp.ServerConfig{
		"kept": broken, "newname": broken, "added": broken,
	}, sess.SessionMCPServers()))
	t.Cleanup(func() {
		if mgr != nil {
			_ = mgr.Close()
		}
	})
	want := []string{"mcp__client__d", "mcp__kept__a"}
	if got := advertisedMCP(sess); !reflect.DeepEqual(got, want) {
		t.Fatalf("after rehydrate: %v, want %v", got, want)
	}

	// The client drops its server (session/load with new mcpServers) and
	// the config drops "kept"; /reload applies both at once.
	sess.UpdateSessionState(func(st *SessionState) { st.Runtime.McpServers = nil })
	writeMCPConfig(t, cwd, `{"mcpServers":{"added":{"command":"/bin/false"}}}`)
	if err := ReloadMCP(context.Background(), &mgr, sess, cwd, "", nil); err != nil {
		t.Fatalf("ReloadMCP: %v", err)
	}
	if got := advertisedMCP(sess); len(got) != 0 {
		t.Fatalf("after reload: %v, want none", got)
	}
	if saved := sess.SessionState().Runtime.McpTools; len(saved) != 0 {
		t.Fatalf("saved after reload: %v, want none", saved)
	}
}

// A rehydrate with no MCP server configured at all forgets every saved list.
func TestMCPTools_NoServersForgetsSaved(t *testing.T) {
	sess, _, _ := newSessionWithAuthPath(t)
	sess.UpdateSessionState(func(st *SessionState) {
		st.Runtime.McpTools = map[string][]ai.Tool{"gone": savedTool("mcp__gone__b")}
	})
	if mgr := StartMCPManager(context.Background(), sess, nil); mgr != nil {
		t.Fatal("manager started with no servers")
	}
	if saved := sess.SessionState().Runtime.McpTools; len(saved) != 0 {
		t.Fatalf("saved = %v, want none", saved)
	}
}
