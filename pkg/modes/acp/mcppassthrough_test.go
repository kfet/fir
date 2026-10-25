package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/mcp/mcptest"
	"github.com/kfet/fir/pkg/session"
)

// newUIEntry starts the fake ui:// MCP server (this test binary re-run with
// MCP_TEST_SERVER=ui, see TestMain) and returns a session entry whose agent
// has the server's tools active, registered in pa under "s1".
func newUIEntry(t *testing.T, pa *firAgent) (*firSession, []agent.AgentTool) {
	t.Helper()
	mgr := mcp.NewManager(map[string]mcp.ServerConfig{"ui": {
		Command: os.Args[0],
		Env:     map[string]string{"MCP_TEST_SERVER": "ui"},
	}}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr.Start(ctx)
	if err := mgr.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	tools := mgr.ToolsByServer()["ui"]
	if len(tools) == 0 {
		t.Fatal("no tools from ui server")
	}
	sess := newMinimalSession(t)
	t.Cleanup(func() { sess.Close() })
	sess.Agent.UpdateTools(func(ts *agent.ToolSet) {
		for _, tl := range tools {
			ts.Add(tl)
		}
	})
	entry := &firSession{termState: newTerminalState(), session: sess, mcpManager: mgr}
	pa.mu.Lock()
	pa.sessions["s1"] = entry
	pa.mu.Unlock()
	return entry, tools
}

func findTool(t *testing.T, tools []agent.AgentTool, name string) agent.AgentTool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found", name)
	return agent.AgentTool{}
}

func extMeta(t *testing.T, meta map[string]any) map[string]any {
	t.Helper()
	v, ok := meta[mcpExtKey].(map[string]any)
	if !ok {
		t.Fatalf("missing _meta[%q] in %v", mcpExtKey, meta)
	}
	return v
}

func TestInitialize_AdvertisesMCPPassthrough(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(acpsdk.InitializeRequest{ProtocolVersion: 1})
	resp, rerr := handler(context.Background(), "initialize", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, _ := json.Marshal(resp)
	var got struct {
		AgentCapabilities struct {
			Meta map[string]struct {
				ToolCallMeta   bool     `json:"toolCallMeta"`
				ResourceBlocks bool     `json:"resourceBlocks"`
				Methods        []string `json:"methods"`
			} `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	c, ok := got.AgentCapabilities.Meta[mcpExtKey]
	if !ok || !c.ToolCallMeta || !c.ResourceBlocks || len(c.Methods) != 2 ||
		c.Methods[0] != mcpMethodReadResource || c.Methods[1] != mcpMethodCallTool {
		t.Fatalf("capability = %s", raw)
	}
}

func TestHandleEvent_MCPToolPassthrough(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry, tools := newUIEntry(t, pa)
	name := mcp.ToolName("ui", mcptest.UIToolName)
	tool := findTool(t, tools, name)
	args := map[string]any{"n": 2.0}

	pa.handleEvent("s1", entry, session.AgentSessionEvent{AgentEvent: &agent.AgentEvent{
		Type: agent.EventToolExecutionStart, ToolCallID: "tc1", ToolName: name, Args: args,
	}})
	res, err := tool.Execute(context.Background(), "", args, nil)
	if err != nil {
		t.Fatal(err)
	}
	pa.handleEvent("s1", entry, session.AgentSessionEvent{AgentEvent: &agent.AgentEvent{
		Type: agent.EventToolExecutionEnd, ToolCallID: "tc1", ToolName: name, Result: res,
	}})

	ups := mc.getUpdates()
	if len(ups) != 2 || ups[0].Update.ToolCall == nil || ups[1].Update.ToolCallUpdate == nil {
		t.Fatalf("updates = %+v", ups)
	}
	start := extMeta(t, ups[0].Update.ToolCall.Meta)
	if start["server"] != "ui" || start["tool"] != mcptest.UIToolName {
		t.Errorf("start meta = %v", start)
	}
	ui, _ := start["toolMeta"].(map[string]any)["ui"].(map[string]any)
	if ui["resourceUri"] != mcptest.UIResourceURI {
		t.Errorf("toolMeta = %v", start["toolMeta"])
	}

	up := ups[1].Update.ToolCallUpdate
	end := extMeta(t, up.Meta)
	if end["server"] != "ui" || end["toolMeta"] == nil {
		t.Errorf("end meta = %v", end)
	}
	if rm, _ := end["resultMeta"].(map[string]any); rm["demo"] != "result-meta" {
		t.Errorf("resultMeta = %v", end["resultMeta"])
	}
	if sc, _ := end["structuredContent"].(map[string]any); sc["n"] != 2.0 {
		t.Errorf("structuredContent = %v", end["structuredContent"])
	}

	var gotRes, gotLink bool
	for _, c := range up.Content {
		if c.Content == nil {
			continue
		}
		if tx := c.Content.Content.Text; tx != nil && tx.Text != "shown" {
			t.Errorf("display text = %q, want only the plain text block", tx.Text)
		}
		if r := c.Content.Content.Resource; r != nil {
			trc := r.Resource.TextResourceContents
			gotRes = trc != nil && trc.Uri == mcptest.UIResourceURI && trc.Text == mcptest.UIHTML
		}
		if l := c.Content.Content.ResourceLink; l != nil {
			gotLink = l.Uri == mcptest.LinkURI
		}
	}
	if !gotRes || !gotLink {
		t.Errorf("resource blocks missing: res=%v link=%v content=%+v", gotRes, gotLink, up.Content)
	}
}

func TestHandleEvent_NonMCPToolHasNoPassthroughMeta(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	entry := &firSession{termState: newTerminalState()}
	pa.handleEvent("s1", entry, session.AgentSessionEvent{AgentEvent: &agent.AgentEvent{
		Type: agent.EventToolExecutionStart, ToolCallID: "tc1", ToolName: "read", Args: map[string]any{"path": "x"},
	}})
	pa.handleEvent("s1", entry, session.AgentSessionEvent{AgentEvent: &agent.AgentEvent{
		Type: agent.EventToolExecutionEnd, ToolCallID: "tc1", ToolName: "read",
		Result: agent.AgentToolResult{Content: nil},
	}})
	for _, u := range mc.getUpdates() {
		if u.Update.ToolCall != nil && u.Update.ToolCall.Meta != nil {
			t.Errorf("start meta = %v", u.Update.ToolCall.Meta)
		}
		if u.Update.ToolCallUpdate != nil && u.Update.ToolCallUpdate.Meta != nil {
			t.Errorf("update meta = %v", u.Update.ToolCallUpdate.Meta)
		}
	}
}

func TestNormalizeToolResult_LiveTextShown(t *testing.T) {
	// Live results arrive as agent.AgentToolResult; their text must reach the
	// client like replayed results do.
	res := agent.AgentToolResult{}
	res.Content = append(res.Content, ai.ToolResultContent{Type: ai.ContentTypeText, Text: "hello"})
	content, _ := BuildToolCallContent("some_ext_tool", nil, normalizeToolResult(res), false)
	if len(content) != 1 || content[0].Content.Content.Text.Text != "hello" {
		t.Fatalf("content = %+v", content)
	}
	if got := normalizeToolResult("x"); got != "x" {
		t.Errorf("non-result changed: %v", got)
	}
}

func TestMCPMethods(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	entry, _ := newUIEntry(t, pa)
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	call := func(method string, p any) (map[string]any, *acpsdk.RequestError) {
		raw, _ := json.Marshal(p)
		resp, rerr := handler(context.Background(), method, raw)
		if rerr != nil {
			return nil, rerr
		}
		b, _ := json.Marshal(resp)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m, nil
	}

	got, rerr := call(mcpMethodReadResource, map[string]any{"sessionId": "s1", "server": "ui", "uri": mcptest.UIResourceURI})
	if rerr != nil {
		t.Fatal(rerr)
	}
	contents, _ := got["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["text"] != mcptest.UIHTML {
		t.Errorf("resources/read = %v", got)
	}

	got, rerr = call(mcpMethodCallTool, map[string]any{"sessionId": "s1", "server": "ui", "name": mcptest.UIToolName, "arguments": map[string]any{"n": 5}})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if sc, _ := got["structuredContent"].(map[string]any); sc["n"] != 5.0 {
		t.Errorf("tools/call = %v", got)
	}

	// Errors: bad params, unknown session, unknown tool.
	for _, tc := range []struct {
		method string
		p      map[string]any
	}{
		{mcpMethodReadResource, map[string]any{"sessionId": "s1", "server": "ui"}},
		{mcpMethodCallTool, map[string]any{"sessionId": "s1", "server": "ui"}},
		{mcpMethodCallTool, map[string]any{"sessionId": "nope", "server": "ui", "name": mcptest.UIToolName}},
		{mcpMethodCallTool, map[string]any{"sessionId": "s1", "server": "ui", "name": "missing"}},
		{mcpMethodReadResource, map[string]any{"sessionId": "s1", "server": "other", "uri": "ui://x"}},
	} {
		if _, rerr := call(tc.method, tc.p); rerr == nil {
			t.Errorf("%s %v: want error", tc.method, tc.p)
		}
	}

	// Permission: a tool removed from the session's active set is refused.
	name := mcp.ToolName("ui", mcptest.UIToolName)
	entry.session.Agent.UpdateTools(func(ts *agent.ToolSet) { ts.Remove(name) })
	if _, rerr := call(mcpMethodCallTool, map[string]any{"sessionId": "s1", "server": "ui", "name": mcptest.UIToolName}); rerr == nil {
		t.Error("inactive tool: want error")
	}
}

// Replayed results carry passthrough data in details even when the MCP
// server is gone, so session/load still forwards it.
func TestMCPToolEndMeta_FromDetailsOnly(t *testing.T) {
	result := map[string]any{"details": map[string]any{mcp.DetailsKey: map[string]any{
		"server": "ui", "tool": "show_app", "structuredContent": map[string]any{"a": 1.0},
		"resources": []any{map[string]any{"type": "resource_link", "uri": mcptest.LinkURI, "name": "doc"}},
	}}}
	v := extMeta(t, mcpToolEndMeta(&firSession{}, "mcp__ui__show_app", result))
	if v["server"] != "ui" || v["tool"] != "show_app" || v["structuredContent"] == nil {
		t.Errorf("meta = %v", v)
	}
	if c := mcpResourceContent(result); len(c) != 1 || c[0].Content.Content.ResourceLink.Uri != mcptest.LinkURI {
		t.Errorf("content = %+v", c)
	}
	if mcpToolEndMeta(nil, "x", map[string]any{}) != nil {
		t.Error("non-MCP result: want nil meta")
	}
}

func TestToolResultToContent_KeepsImageData(t *testing.T) {
	got := toolResultToContent([]ai.ToolResultContent{{Type: ai.ContentTypeImage, Data: "AAAA", MimeType: "image/png"}})
	m := got[0].(map[string]any)
	if m["data"] != "AAAA" || m["mimeType"] != "image/png" {
		t.Errorf("image block = %v", m)
	}
}

func TestMCPDisplayResult_DropsFlattenedAfterRoundTrip(t *testing.T) {
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "<html>"}},
		"details": map[string]any{mcp.DetailsKey: map[string]any{"flattened": []any{1.0}}},
	}
	if got := extractResultText(mcpDisplayResult(result)); got != "a" {
		t.Errorf("display = %q", got)
	}
	if len(result["content"].([]any)) != 2 {
		t.Error("input mutated")
	}
	plain := map[string]any{"content": []any{}}
	if got := mcpDisplayResult(plain); got.(map[string]any)["content"] == nil {
		t.Error("plain result changed")
	}
}

func TestNormalizeToolResult_MatchesReplayShape(t *testing.T) {
	res := agent.AgentToolResult{
		Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "file body"}},
		Details: map[string]any{"k": "v"},
	}
	live := normalizeToolResult(res)
	replay := map[string]any{"content": toolResultToContent(res.Content), "isError": false, "details": res.Details}
	lb, _ := json.Marshal(live)
	rb, _ := json.Marshal(replay)
	if string(lb) != string(rb) {
		t.Fatalf("live %s != replay %s", lb, rb)
	}
	lc, _ := BuildToolCallContent("read", nil, live, false)
	rc, _ := BuildToolCallContent("read", nil, replay, false)
	if len(lc) != 1 || len(rc) != 1 || lc[0].Content.Content.Text.Text != rc[0].Content.Content.Text.Text {
		t.Fatalf("content live=%+v replay=%+v", lc, rc)
	}
	if p := (*agent.AgentToolResult)(nil); normalizeToolResult(p) != p {
		t.Error("nil pointer changed")
	}
	if m, _ := normalizeToolResult(&res).(map[string]any); m["details"] == nil {
		t.Error("pointer result not normalised")
	}
}
