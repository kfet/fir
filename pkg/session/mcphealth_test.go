package session

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/sections"
)

// An unhealthy configured server is listed in the startup sections block on
// the very first turn; once fixed, the next turn replaces the section with a
// cleared version.
func TestMCPHealthSection_FirstTurnThenClearedAfterReload(t *testing.T) {
	h := newSectionsHarness(t)
	ctx := context.Background()
	mgr := StartMCPManager(ctx, h.s, map[string]mcp.ServerConfig{
		"broken": {Command: "/nonexistent/fir-test-mcp-server"},
	})
	t.Cleanup(func() { _ = mgr.Close() })

	first := userText(h.prompt(t, "hello")[0])
	if !strings.HasPrefix(first, sections.BlockHeader) ||
		!strings.Contains(first, "[section=mcp]\nMCP server broken: configured, ERROR, tools unavailable -> fir command: /mcp reload broken") {
		t.Fatalf("first turn lacks the mcp section: %q", first)
	}

	info := h.s.Introspect(IntrospectOptions{})
	if len(info.MCP.Servers) != 1 || info.MCP.Servers[0].State != mcp.StateError || info.MCP.Servers[0].Fix != "/mcp reload broken" {
		t.Fatalf("introspect servers = %+v", info.MCP.Servers)
	}

	if _, err := mgr.Reload(ctx, map[string]mcp.ServerConfig{}); err != nil {
		t.Fatal(err)
	}
	msgs := h.prompt(t, "again")
	if got := userText(msgs[len(msgs)-2]); !strings.HasPrefix(got, "[SYS_EXT section=mcp v2 replaces v1]\n(section cleared") {
		t.Fatalf("expected cleared mcp section, got %q", got)
	}
}

func TestMCPHealthSection_NoManagerNoSection(t *testing.T) {
	h := newSectionsHarness(t)
	if strings.Contains(userText(h.prompt(t, "hi")[0]), "[section=mcp]") {
		t.Fatal("unexpected mcp section without MCP")
	}
	if got := h.s.Introspect(IntrospectOptions{}).MCP.Servers; got == nil || len(got) != 0 {
		t.Fatalf("servers must be an empty list, got %#v", got)
	}
}

func TestSubscribeMCPHealth(t *testing.T) {
	h := newSectionsHarness(t)
	got := make(chan []mcp.ServerHealth, 16)
	unsub := h.s.SubscribeMCPHealth(func(hs []mcp.ServerHealth) { got <- hs })
	mgr := StartMCPManager(context.Background(), h.s, map[string]mcp.ServerConfig{
		"broken": {Command: "/nonexistent/fir-test-mcp-server"},
	})
	t.Cleanup(func() { _ = mgr.Close() })
	for hs := range got {
		if len(hs) == 1 && hs[0].State == mcp.StateError {
			break
		}
	}
	unsub()
}
