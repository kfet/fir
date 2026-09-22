package interactive

import (
	"errors"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
)

func TestBuildMCPNoticeCollapsesToOneLine(t *testing.T) {
	order := []string{"slack", "atlassian", "daisy-main"}
	states := map[string]*mcpServerState{
		"slack":      {seen: true, kind: mcp.ServerReady},
		"atlassian":  {seen: true, kind: mcp.ServerReady, reconnects: 4},
		"daisy-main": {seen: true, kind: mcp.ServerConnecting},
	}
	summary, warnings := buildMCPNotice(order, states)
	want := "MCP: slack, atlassian (reconnected 4×), daisy-main (connecting)"
	if summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestBuildMCPNoticeReportsErrorsSeparately(t *testing.T) {
	order := []string{"slack", "broken"}
	states := map[string]*mcpServerState{
		"slack":  {seen: true, kind: mcp.ServerReady},
		"broken": {seen: true, kind: mcp.ServerDisconnected, err: errors.New("pipe closed")},
	}
	summary, warnings := buildMCPNotice(order, states)
	if summary != "MCP: slack" {
		t.Errorf("summary = %q", summary)
	}
	if len(warnings) != 1 || warnings[0] != `MCP server "broken" disconnected: pipe closed` {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestMCPSuffixReadyIsBare(t *testing.T) {
	if got := mcpSuffix(&mcpServerState{seen: true, kind: mcp.ServerReady}); got != "" {
		t.Errorf("suffix = %q, want empty", got)
	}
}
