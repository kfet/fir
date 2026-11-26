package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/mcp/mcptest"
)

// wrappedDial is inMemoryDial with a server-side transport wrapper.
func wrappedDial(t *testing.T, server *sdk.Server, wrap func(sdk.Transport) sdk.Transport) func(string, ServerConfig) (sdk.Transport, error) {
	t.Helper()
	conns := connsForServer(server)
	t.Cleanup(func() { forgetServerConns(server) })
	return func(string, ServerConfig) (sdk.Transport, error) {
		st, ct := sdk.NewInMemoryTransports()
		go func() { _ = server.Run(context.Background(), wrap(st)) }()
		return &breakableTransport{inner: ct, conns: conns}, nil
	}
}

func toolByName(tools []agent.AgentTool, name string) *agent.AgentTool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func skillMD(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
}

func newSkillsFixture(t *testing.T, cfg ServerConfig, unlisted ...string) (*mcptest.SkillsServer, *Manager, []agent.AgentTool) {
	t.Helper()
	ss := mcptest.NewSkillsServer(map[string]map[string]string{
		"git-workflow":         {"SKILL.md": skillMD("git-workflow", "Git rules", "Use refs/GUIDE.md."), "refs/GUIDE.md": "guide text"},
		"acme/billing/refunds": {"SKILL.md": skillMD("refunds", "Billing refunds", "billing body")},
		"acme/support/refunds": {"SKILL.md": skillMD("refunds", "Support refunds", "support body")},
		"hidden":               {"SKILL.md": skillMD("hidden", "Not listed", "hidden body")},
	}, unlisted...)
	ss.PageSize = 2
	mgr := NewManager(map[string]ServerConfig{"sk": cfg}, false)
	mgr.dialFn = wrappedDial(t, ss.Server, ss.Transport)
	tools := startAndWait(t, mgr, context.Background())
	t.Cleanup(func() { _ = mgr.Close() })
	return ss, mgr, tools
}

func runTool(t *testing.T, tl *agent.AgentTool, args map[string]any) agent.AgentToolResult {
	t.Helper()
	require.NotNil(t, tl)
	res, err := tl.Execute(context.Background(), "", args, nil)
	require.NoError(t, err)
	return res
}

func TestSkills_ListAndLoad(t *testing.T) {
	ss, mgr, tools := newSkillsFixture(t, ServerConfig{}, "hidden")

	skills := mgr.SkillsByServer()["sk"]
	names := []string{}
	for _, s := range skills {
		names = append(names, s.Name)
	}
	// Paginated (page size 2), unlisted skill absent, colliding names
	// qualified by path.
	assert.ElementsMatch(t, []string{"sk:git-workflow", "sk:acme/billing/refunds", "sk:acme/support/refunds"}, names)
	// Listing fetches no files.
	assert.Zero(t, ss.Reads("skill://git-workflow/SKILL.md"))

	section := FormatMCPSkills(mgr.SkillsByServer())
	assert.Contains(t, section, "sk:git-workflow — Git rules")
	assert.Contains(t, section, "mcp__sk__read_skill uri=skill://git-workflow/SKILL.md")

	rs := toolByName(tools, "mcp__sk__read_skill")
	res := runTool(t, rs, map[string]any{"uri": "skill://git-workflow/SKILL.md"})
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "Use refs/GUIDE.md.")
	assert.Contains(t, resultText(res), `from server "sk"`)

	res = runTool(t, rs, map[string]any{"uri": "skill://git-workflow/SKILL.md", "path": "refs/GUIDE.md"})
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "guide text")

	// Cached: a second load does not read again.
	runTool(t, rs, map[string]any{"uri": "skill://git-workflow/SKILL.md"})
	assert.Equal(t, 1, ss.Reads("skill://git-workflow/SKILL.md"))

	// Not in the manifest, and path escapes are confined to the skill.
	res = runTool(t, rs, map[string]any{"uri": "skill://git-workflow/SKILL.md", "path": "../acme/billing/refunds/SKILL.md"})
	assert.True(t, res.IsError)

	// An unlisted skill loads through skills/get.
	res = runTool(t, rs, map[string]any{"uri": "skill://hidden/SKILL.md"})
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "hidden body")

	// Unknown skill: error, not a crash.
	res = runTool(t, rs, map[string]any{"uri": "skill://nope/SKILL.md"})
	assert.True(t, res.IsError)
}

func TestSkills_DigestMismatchRejected(t *testing.T) {
	ss, _, tools := newSkillsFixture(t, ServerConfig{})
	ss.Tamper("skill://git-workflow/refs/GUIDE.md", "evil text!")
	res := runTool(t, toolByName(tools, "mcp__sk__read_skill"), map[string]any{"uri": "skill://git-workflow/SKILL.md", "path": "refs/GUIDE.md"})
	assert.True(t, res.IsError)
	assert.NotContains(t, resultText(res), "evil")
}

func TestSkills_OptOut(t *testing.T) {
	_, mgr, tools := newSkillsFixture(t, ServerConfig{DisableSkills: true})
	assert.Nil(t, toolByName(tools, "mcp__sk__read_skill"))
	assert.Empty(t, mgr.SkillsByServer()["sk"])
}

func TestSkills_NotDeclaredMeansNoCalls(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "plain", Version: "0"}, nil)
	server.AddResource(&sdk.Resource{URI: "skill://x/SKILL.md", Name: "x"}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{}, nil
	})
	var mu sync.Mutex
	called := false
	mgr := NewManager(map[string]ServerConfig{"p": {}}, false)
	mgr.dialFn = wrappedDial(t, server, func(st sdk.Transport) sdk.Transport {
		return &mcptest.ExtTransport{Inner: st, Methods: map[string]mcptest.ExtHandler{
			"skills/list": func(context.Context, json.RawMessage) (any, error) {
				mu.Lock()
				called = true
				mu.Unlock()
				return map[string]any{"skills": []any{}}, nil
			},
		}}
	})
	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	assert.Nil(t, toolByName(tools, "mcp__p__read_skill"))
	mu.Lock()
	defer mu.Unlock()
	assert.False(t, called)
}

func TestSkillEntry_Validate(t *testing.T) {
	good := `{"uri":"skill://a/b/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":[{"uri":"skill://a/b/SKILL.md","digest":"sha256:` + strings.Repeat("0", 64) + `","size":1}]}`
	var e skillEntry
	require.NoError(t, json.Unmarshal([]byte(good), &e))
	require.NoError(t, e.validate())

	for name, raw := range map[string]string{
		"no resources": `{"uri":"skill://b/SKILL.md","frontmatter":{"name":"b","description":"d"}}`,
	} {
		var e skillEntry
		assert.Error(t, json.Unmarshal([]byte(raw), &e), name)
	}
	for name, raw := range map[string]string{
		"name mismatch": `{"uri":"skill://c/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":"dynamic"}`,
		"outside":       `{"uri":"skill://b/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":[{"uri":"skill://b/SKILL.md","digest":"sha256:` + strings.Repeat("0", 64) + `","size":1},{"uri":"skill://c/x","digest":"sha256:` + strings.Repeat("0", 64) + `","size":1}]}`,
		"bad digest":    `{"uri":"skill://b/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":[{"uri":"skill://b/SKILL.md","digest":"md5:x","size":1}]}`,
		"no SKILL.md":   `{"uri":"skill://b/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":[]}`,
		"too big":       `{"uri":"skill://b/SKILL.md","frontmatter":{"name":"b","description":"d"},"resources":[{"uri":"skill://b/SKILL.md","digest":"sha256:` + strings.Repeat("0", 64) + `","size":99999999}]}`,
	} {
		var e skillEntry
		require.NoError(t, json.Unmarshal([]byte(raw), &e), name)
		assert.Error(t, e.validate(), name)
	}
}

func TestCheckFrontmatter(t *testing.T) {
	md := []byte("---\nname: x\ndescription: y\nmetadata:\n  version: \"1\"\n---\nbody")
	assert.NoError(t, checkFrontmatter(md, map[string]any{"name": "x", "description": "y", "metadata": map[string]any{"version": "1"}}))
	assert.Error(t, checkFrontmatter(md, map[string]any{"name": "x", "description": "other"}))
	assert.Error(t, checkFrontmatter([]byte("no frontmatter"), map[string]any{}))
}

// tasksFixture serves a "slow" tool whose result arrives through a task.
func tasksFixture(t *testing.T, cfg ServerConfig, setup func(*mcptest.TasksServer, *Manager)) (*mcptest.TasksServer, *agent.AgentTool) {
	t.Helper()
	caps := &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}}
	caps.AddExtension(TasksExtensionID, map[string]any{})
	server := sdk.NewServer(&sdk.Implementation{Name: "tasks-srv", Version: "0"}, &sdk.ServerOptions{Capabilities: caps})
	server.AddTool(&sdk.Tool{Name: "slow", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done!"}}}, nil
		})
	ts := mcptest.NewTasksServer(server)
	mgr := NewManager(map[string]ServerConfig{"ts": cfg}, false)
	mgr.dialFn = wrappedDial(t, server, ts.Transport)
	if setup != nil {
		setup(ts, mgr)
	}
	tools := startAndWait(t, mgr, context.Background())
	t.Cleanup(func() { _ = mgr.Close() })
	return ts, toolByName(tools, "mcp__ts__slow")
}

func TestTasks_PolledToCompletion(t *testing.T) {
	_, tl := tasksFixture(t, ServerConfig{}, nil)
	res := runTool(t, tl, nil)
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "done!")
}

func TestTasks_Failed(t *testing.T) {
	_, tl := tasksFixture(t, ServerConfig{}, func(ts *mcptest.TasksServer, _ *Manager) { ts.Fail = true })
	res, err := tl.Execute(context.Background(), "", nil, nil)
	if err == nil {
		assert.True(t, res.IsError)
		assert.Contains(t, resultText(res), "boom")
	} else {
		assert.Contains(t, err.Error(), "boom")
	}
}

func TestTasks_InputRequiredUsesElicitation(t *testing.T) {
	ts, tl := tasksFixture(t, ServerConfig{}, func(ts *mcptest.TasksServer, mgr *Manager) {
		ts.Elicit = &sdk.ElicitParams{Mode: "form", Message: "Proceed?", RequestedSchema: map[string]any{
			"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
		}}
		mgr.SetElicitation(func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			return ElicitFormResult(map[string]any{"ok": true}), nil
		}, ElicitModes{Form: true})
	})
	res := runTool(t, tl, nil)
	require.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "done!")
	responses, _ := ts.Snapshot()
	require.Len(t, responses, 1)
	assert.JSONEq(t, `{"confirm":{"action":"accept","content":{"ok":true}}}`, string(responses[0]))
}

func TestTasks_InputRequiredHeadlessDeclines(t *testing.T) {
	ts, tl := tasksFixture(t, ServerConfig{}, func(ts *mcptest.TasksServer, _ *Manager) {
		ts.Elicit = &sdk.ElicitParams{Mode: "form", Message: "Proceed?", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
	})
	runTool(t, tl, nil)
	responses, _ := ts.Snapshot()
	require.Len(t, responses, 1)
	assert.JSONEq(t, `{"confirm":{"action":"decline"}}`, string(responses[0]))
}

func TestTasks_OptOutPassesResultThrough(t *testing.T) {
	// With tasks off, fir does not interpret a task handle: the SDK sees
	// the raw result. (A conforming server would not send one.)
	_, tl := tasksFixture(t, ServerConfig{DisableTasks: true}, nil)
	res, err := tl.Execute(context.Background(), "", nil, nil)
	if err == nil {
		assert.NotContains(t, resultText(res), "done!")
	}
}

func TestClientCapabilities(t *testing.T) {
	caps := clientCapabilities(ServerConfig{}, nil)
	assert.Contains(t, caps.Extensions, TasksExtensionID)
	assert.Nil(t, caps.Elicitation)
	caps = clientCapabilities(ServerConfig{DisableTasks: true}, &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}})
	assert.NotContains(t, caps.Extensions, TasksExtensionID)
	assert.NotNil(t, caps.Elicitation.URL)
}

func TestStripElicitationCap(t *testing.T) {
	in := `{"capabilities":{"elicitation":{"form":{}},"roots":{}},"_meta":{"io.modelcontextprotocol/clientCapabilities":{"elicitation":{},"extensions":{}}}}`
	out, changed := stripElicitationCap(json.RawMessage(in))
	require.True(t, changed)
	assert.JSONEq(t, `{"capabilities":{"roots":{}},"_meta":{"io.modelcontextprotocol/clientCapabilities":{"extensions":{}}}}`, string(out))
	_, changed = stripElicitationCap(json.RawMessage(`{"name":"x"}`))
	assert.False(t, changed)
}

// capsRecorder records the client capabilities a server sees.
func capsRecorder(t *testing.T, mgr *Manager) chan json.RawMessage {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "rec", Version: "0"}, nil)
	seen := make(chan json.RawMessage, 16)
	mgr.dialFn = wrappedDial(t, server, func(st sdk.Transport) sdk.Transport {
		return &recordTransport{inner: st, seen: seen}
	})
	return seen
}

type recordTransport struct {
	inner sdk.Transport
	seen  chan json.RawMessage
}

func (r *recordTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := r.inner.Connect(ctx)
	return &recordConn{Connection: c, seen: r.seen}, err
}

type recordConn struct {
	sdk.Connection
	seen chan json.RawMessage
}

func (c *recordConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if req, ok := msg.(*jsonrpc.Request); ok && req.ID.IsValid() {
		select {
		case c.seen <- req.Params:
		default:
		}
	}
	return msg, err
}

func anyElicitation(seen chan json.RawMessage) bool {
	for {
		select {
		case p := <-seen:
			if strings.Contains(string(p), `"elicitation"`) {
				return true
			}
		default:
			return false
		}
	}
}

func TestElicitation_AdvertisedOnlyWithFrontEnd(t *testing.T) {
	headless := NewManager(map[string]ServerConfig{"r": {}}, false)
	seen := capsRecorder(t, headless)
	startAndWait(t, headless, context.Background())
	_ = headless.Close()
	assert.False(t, anyElicitation(seen), "headless must not advertise elicitation")

	ui := NewManager(map[string]ServerConfig{"r": {}}, false)
	seen = capsRecorder(t, ui)
	ui.SetElicitation(DefaultElicitFn, ElicitModes{Form: true})
	startAndWait(t, ui, context.Background())
	_ = ui.Close()
	assert.True(t, anyElicitation(seen), "a front end must advertise elicitation")
}

func TestElicitFieldsAndParse(t *testing.T) {
	req := &sdk.ElicitRequest{Params: &sdk.ElicitParams{RequestedSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"age":   map[string]any{"type": "integer"},
			"name":  map[string]any{"type": "string", "title": "Name"},
			"color": map[string]any{"type": "string", "enum": []any{"red", "blue"}},
			"ok":    map[string]any{"type": "boolean"},
		},
		"required": []any{"name"},
	}}}
	fields, err := ElicitFields(req)
	require.NoError(t, err)
	require.Len(t, fields, 4)
	assert.Equal(t, "name", fields[0].Name)
	assert.True(t, fields[0].Required)
	assert.Equal(t, "Name", fields[0].Label())

	byName := map[string]ElicitField{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	v, ok, err := ParseElicitValue(byName["age"], "42")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(42), v)
	_, _, err = ParseElicitValue(byName["age"], "x")
	assert.Error(t, err)
	v, _, _ = ParseElicitValue(byName["ok"], "yes")
	assert.Equal(t, true, v)
	_, _, err = ParseElicitValue(byName["color"], "green")
	assert.Error(t, err)
	_, ok, err = ParseElicitValue(byName["name"], "  ")
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestLateElicit(t *testing.T) {
	var l LateElicit
	res, err := l.Ask(context.Background(), &sdk.ElicitRequest{Params: &sdk.ElicitParams{}})
	require.NoError(t, err)
	assert.Equal(t, "decline", res.Action)
	l.Set(func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		return &sdk.ElicitResult{Action: "accept"}, nil
	})
	res, _ = l.Ask(context.Background(), &sdk.ElicitRequest{Params: &sdk.ElicitParams{}})
	assert.Equal(t, "accept", res.Action)
}

func TestInheritLocalSettings_ExtensionOptOuts(t *testing.T) {
	got := InheritLocalSettings(ServerConfig{URL: "u"}, ServerConfig{DisableSkills: true, DisableTasks: true})
	assert.True(t, got.DisableSkills)
	assert.True(t, got.DisableTasks)
	assert.Equal(t, "u", got.URL)
}

func TestFormatMCPSkills_Bounded(t *testing.T) {
	assert.Empty(t, FormatMCPSkills(map[string][]MCPSkill{"x": nil}))
	var list []MCPSkill
	for i := 0; i < mcpSkillsPromptMax+3; i++ {
		list = append(list, MCPSkill{Name: "s:n", Description: "line1\nline2 " + strings.Repeat("d", 2000), URI: "u", Tool: "t"})
	}
	out := FormatMCPSkills(map[string][]MCPSkill{"s": list})
	assert.Contains(t, out, "+3 more s skills")
	assert.Contains(t, out, "line1 line2 ")
	assert.NotContains(t, out, strings.Repeat("d", mcpSkillDescMax))
	assert.Equal(t, mcpSkillsPromptMax, strings.Count(out, "load: "))
}

func TestTasks_CancelledCallCancelsTask(t *testing.T) {
	ts, tl := tasksFixture(t, ServerConfig{}, func(ts *mcptest.TasksServer, _ *Manager) { ts.Hold = true })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := tl.Execute(ctx, "", nil, nil)
	require.Error(t, err)
	require.Eventually(t, func() bool {
		_, cancelled := ts.Snapshot()
		return len(cancelled) == 1
	}, 15*time.Second, 10*time.Millisecond)
}
