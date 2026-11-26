package mcptest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawClient talks JSON-RPC to a server transport without the SDK client.
type rawClient struct {
	t    *testing.T
	conn sdk.Connection
	next int64
}

func dialRaw(t *testing.T, server *sdk.Server, wrap func(sdk.Transport) sdk.Transport) *rawClient {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = server.Run(ctx, wrap(st)); close(done) }()
	conn, err := ct.Connect(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(); cancel(); <-done })
	return &rawClient{t: t, conn: conn}
}

// call sends method and returns the response.
func (c *rawClient) call(method string, params any) *jsonrpc.Response {
	c.t.Helper()
	c.next++
	id, _ := jsonrpc.MakeID(float64(c.next))
	b, _ := json.Marshal(params)
	require.NoError(c.t, c.conn.Write(context.Background(), &jsonrpc.Request{ID: id, Method: method, Params: b}))
	for {
		msg, err := c.conn.Read(context.Background())
		require.NoError(c.t, err)
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.ID == id {
			return resp
		}
	}
}

func (c *rawClient) notify(method string) {
	require.NoError(c.t, c.conn.Write(context.Background(), &jsonrpc.Request{Method: method, Params: json.RawMessage(`{}`)}))
}

func decode(t *testing.T, resp *jsonrpc.Response) map[string]any {
	t.Helper()
	require.Nil(t, resp.Error)
	var m map[string]any
	require.NoError(t, json.Unmarshal(resp.Result, &m))
	return m
}

func initialize(c *rawClient) {
	c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}})
	c.notify("notifications/initialized")
}

func TestSkillsServer(t *testing.T) {
	ss := NewSkillsServer(map[string]map[string]string{
		"a":    {"SKILL.md": "---\nname: a\ndescription: A\n---\nbody", "x.md": "x"},
		"b/c":  {"SKILL.md": "---\nname: c\ndescription: C\n---\n"},
		"bad":  {"SKILL.md": "no frontmatter"},
		"bad2": {"SKILL.md": "---\nunterminated"},
		"h":    {"SKILL.md": "---\nname: h\ndescription: H\n---\n"},
	}, "h")
	ss.PageSize = 2
	c := dialRaw(t, ss.Server, ss.Transport)

	page := decode(t, c.call("skills/list", map[string]any{}))
	assert.Len(t, page["skills"], 2)
	assert.Equal(t, "2", page["nextCursor"])
	page = decode(t, c.call("skills/list", map[string]any{"cursor": "2"}))
	assert.Len(t, page["skills"], 2)
	assert.Nil(t, page["nextCursor"])

	got := decode(t, c.call("skills/get", map[string]any{"uri": "skill://h/SKILL.md"}))
	entry := got["skill"].(map[string]any)
	assert.Equal(t, map[string]any{"name": "h", "description": "H"}, entry["frontmatter"])
	got = decode(t, c.call("skills/get", map[string]any{"uri": "skill://bad/SKILL.md"}))
	assert.Empty(t, got["skill"].(map[string]any)["frontmatter"])

	resp := c.call("skills/get", map[string]any{"uri": "skill://nope/SKILL.md"})
	require.NotNil(t, resp.Error)
	resp = c.call("skills/get", map[string]any{"uri": "skill://a/x.md"})
	require.NotNil(t, resp.Error)

	initialize(c)
	read := decode(t, c.call("resources/read", map[string]any{"uri": "skill://a/x.md"}))
	assert.Equal(t, "x", read["contents"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, 1, ss.Reads("skill://a/x.md"))
	ss.Tamper("skill://a/x.md", "y")
	read = decode(t, c.call("resources/read", map[string]any{"uri": "skill://a/x.md"}))
	assert.Equal(t, "y", read["contents"].([]any)[0].(map[string]any)["text"])

	// Unknown resource: the handler reports not found.
	_, err := ss.read(context.Background(), &sdk.ReadResourceRequest{Params: &sdk.ReadResourceParams{URI: "skill://zzz"}})
	assert.Error(t, err)
	assert.Equal(t, "sha256:2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881", Digest([]byte("x")))
}

func TestExtTransport_ErrorsAndPassThrough(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "s", Version: "0"}, nil)
	c := dialRaw(t, server, func(st sdk.Transport) sdk.Transport {
		return &ExtTransport{Inner: st, Methods: map[string]ExtHandler{
			"x/wire": func(context.Context, json.RawMessage) (any, error) {
				return nil, &jsonrpc.Error{Code: -32602, Message: "bad"}
			},
			"x/plain": func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("oops") },
		}}
	})
	resp := c.call("x/wire", nil)
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Error(), "bad")
	resp = c.call("x/plain", nil)
	require.NotNil(t, resp.Error)
	assert.Contains(t, resp.Error.Error(), "oops")
	// Unknown methods and notifications reach the SDK.
	initialize(c)
	assert.Nil(t, c.call("ping", map[string]any{}).Error)

	_, err := (&ExtTransport{Inner: fakeTransport{err: errors.New("dial")}}).Connect(context.Background())
	assert.Error(t, err)
}

func tasksServer(t *testing.T) (*TasksServer, *rawClient) {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "s", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done"}}}, nil
		})
	ts := NewTasksServer(server)
	c := dialRaw(t, server, ts.Transport)
	initialize(c)
	return ts, c
}

func startTask(t *testing.T, c *rawClient) string {
	t.Helper()
	h := decode(t, c.call("tools/call", map[string]any{"name": "slow", "arguments": map[string]any{}}))
	assert.Equal(t, "task", h["resultType"])
	return h["taskId"].(string)
}

func TestTasksServer_InputThenComplete(t *testing.T) {
	ts, c := tasksServer(t)
	ts.Elicit = &sdk.ElicitParams{Message: "ok?"}
	// Other results are not rewritten.
	assert.Contains(t, decode(t, c.call("tools/list", map[string]any{})), "tools")
	id := startTask(t, c)

	get := func() map[string]any { return decode(t, c.call("tasks/get", map[string]any{"taskId": id})) }
	assert.Equal(t, "working", get()["status"])
	st := get()
	assert.Equal(t, "input_required", st["status"])
	assert.Contains(t, st["inputRequests"], "confirm")
	decode(t, c.call("tasks/update", map[string]any{"taskId": id, "inputResponses": map[string]any{"confirm": map[string]any{"action": "accept"}}}))
	st = get()
	assert.Equal(t, "completed", st["status"])
	assert.NotNil(t, st["result"])

	decode(t, c.call("tasks/cancel", map[string]any{"taskId": id}))
	responses, cancelled := ts.Snapshot()
	assert.Len(t, responses, 1)
	assert.Equal(t, []string{id}, cancelled)

	assert.NotNil(t, c.call("tasks/get", map[string]any{"taskId": "nope"}).Error)
	// Updating an unknown task is acknowledged.
	decode(t, c.call("tasks/update", map[string]any{"taskId": "nope"}))
}

func TestTasksServer_Fail(t *testing.T) {
	ts, c := tasksServer(t)
	ts.Fail = true
	id := startTask(t, c)
	decode(t, c.call("tasks/get", map[string]any{"taskId": id}))
	st := decode(t, c.call("tasks/get", map[string]any{"taskId": id}))
	assert.Equal(t, "failed", st["status"])
}

func TestTasksServer_Hold(t *testing.T) {
	ts, c := tasksServer(t)
	ts.Hold = true
	id := startTask(t, c)
	for range 3 {
		assert.Equal(t, "working", decode(t, c.call("tasks/get", map[string]any{"taskId": id}))["status"])
	}
}
