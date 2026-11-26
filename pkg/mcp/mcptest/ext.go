package mcptest

// Fake servers for the MCP skills and tasks extensions. The go-sdk server
// knows neither, so ExtTransport answers the extension methods on the
// server's connection before the SDK sees them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SkillsExt and TasksExt are the extension identifiers.
const (
	SkillsExt = "io.modelcontextprotocol/skills"
	TasksExt  = "io.modelcontextprotocol/tasks"
)

// ExtHandler answers one extension method. A *jsonrpc.Error return is sent
// as that error.
type ExtHandler func(ctx context.Context, params json.RawMessage) (any, error)

// ExtTransport wraps a server transport: requests for a method in Methods
// are answered by its handler; RewriteResult, when set, may replace the
// result the SDK sends for a request (by method).
type ExtTransport struct {
	Inner         sdk.Transport
	Methods       map[string]ExtHandler
	RewriteResult func(method string, result json.RawMessage) json.RawMessage
}

func (t *ExtTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := t.Inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &extServerConn{Connection: c, t: t, methods: map[string]string{}}, nil
}

type extServerConn struct {
	sdk.Connection
	t       *ExtTransport
	mu      sync.Mutex
	methods map[string]string // request id → method, for RewriteResult
}

func idString(id jsonrpc.ID) string { return fmt.Sprintf("%T:%v", id.Raw(), id.Raw()) }

func (c *extServerConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok || !req.ID.IsValid() {
			return msg, nil
		}
		h, ok := c.t.Methods[req.Method]
		if !ok {
			if c.t.RewriteResult != nil {
				c.mu.Lock()
				c.methods[idString(req.ID)] = req.Method
				c.mu.Unlock()
			}
			return msg, nil
		}
		go func() {
			res, herr := h(ctx, req.Params)
			resp := &jsonrpc.Response{ID: req.ID}
			if herr != nil {
				if we, ok := herr.(*jsonrpc.Error); ok {
					resp.Error = we
				} else {
					resp.Error = &jsonrpc.Error{Code: -32603, Message: herr.Error()}
				}
			} else {
				b, _ := json.Marshal(res)
				resp.Result = b
			}
			_ = c.Connection.Write(ctx, resp)
		}()
	}
}

func (c *extServerConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if resp, ok := msg.(*jsonrpc.Response); ok && c.t.RewriteResult != nil && len(resp.Result) > 0 {
		c.mu.Lock()
		method := c.methods[idString(resp.ID)]
		delete(c.methods, idString(resp.ID))
		c.mu.Unlock()
		if method != "" {
			cp := *resp
			cp.Result = c.t.RewriteResult(method, resp.Result)
			msg = &cp
		}
	}
	return c.Connection.Write(ctx, msg)
}

// Digest returns the skills-extension digest of b.
func Digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// SkillsServer is a fake server for the skills extension. Skills maps a
// skill path ("git-workflow", "acme/billing/refunds") to its files (path
// relative to the skill root → content); every skill needs a SKILL.md.
type SkillsServer struct {
	Server *sdk.Server

	mu     sync.Mutex
	files  map[string][]byte // uri → served bytes
	skills map[string]map[string][]byte
	// Unlisted skill paths are served by skills/get and resources/read but
	// left out of skills/list.
	unlisted map[string]bool
	// Reads counts resources/read calls by URI.
	reads map[string]int
	// PageSize splits skills/list into pages (0 = one page).
	PageSize int
}

// NewSkillsServer builds a skills server. unlisted names skill paths to
// keep out of skills/list.
func NewSkillsServer(skills map[string]map[string]string, unlisted ...string) *SkillsServer {
	caps := &sdk.ServerCapabilities{Resources: &sdk.ResourceCapabilities{}}
	caps.AddExtension(SkillsExt, map[string]any{})
	ss := &SkillsServer{
		Server:   sdk.NewServer(&sdk.Implementation{Name: "skills-srv", Version: "1"}, &sdk.ServerOptions{Capabilities: caps}),
		files:    map[string][]byte{},
		skills:   map[string]map[string][]byte{},
		unlisted: map[string]bool{},
		reads:    map[string]int{},
	}
	for _, u := range unlisted {
		ss.unlisted[u] = true
	}
	for sp, files := range skills {
		ss.skills[sp] = map[string][]byte{}
		for rel, content := range files {
			ss.skills[sp][rel] = []byte(content)
			uri := "skill://" + sp + "/" + rel
			ss.files[uri] = []byte(content)
			ss.Server.AddResource(&sdk.Resource{URI: uri, Name: path.Base(rel), MIMEType: "text/markdown"}, ss.read)
		}
	}
	return ss
}

func (ss *SkillsServer) read(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	uri := req.Params.URI
	ss.reads[uri]++
	b, ok := ss.files[uri]
	if !ok {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: string(b)}}}, nil
}

// Tamper changes what resources/read serves for uri without changing the
// listing.
func (ss *SkillsServer) Tamper(uri, content string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.files[uri] = []byte(content)
}

// Reads returns how often uri was read.
func (ss *SkillsServer) Reads(uri string) int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.reads[uri]
}

// entry builds the skills/list entry of skill path sp from the original
// (untampered) files.
func (ss *SkillsServer) entry(sp string) map[string]any {
	files := ss.skills[sp]
	fm := frontmatter(string(files["SKILL.md"]))
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	res := make([]map[string]any, 0, len(rels))
	for _, rel := range rels {
		res = append(res, map[string]any{
			"uri":    "skill://" + sp + "/" + rel,
			"digest": Digest(files[rel]),
			"size":   len(files[rel]),
		})
	}
	return map[string]any{"uri": "skill://" + sp + "/SKILL.md", "frontmatter": fm, "resources": res}
}

// frontmatter parses the simple "key: value" frontmatter of a SKILL.md.
func frontmatter(s string) map[string]any {
	out := map[string]any{}
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return out
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// Transport wraps a server transport so it answers skills/list and
// skills/get.
func (ss *SkillsServer) Transport(inner sdk.Transport) sdk.Transport {
	return &ExtTransport{Inner: inner, Methods: map[string]ExtHandler{
		"skills/list": ss.list,
		"skills/get":  ss.get,
	}}
}

func (ss *SkillsServer) list(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(params, &p)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var paths []string
	for sp := range ss.skills {
		if !ss.unlisted[sp] {
			paths = append(paths, sp)
		}
	}
	sort.Strings(paths)
	start := 0
	if p.Cursor != "" {
		fmt.Sscan(p.Cursor, &start)
	}
	end := len(paths)
	if ss.PageSize > 0 && start+ss.PageSize < end {
		end = start + ss.PageSize
	}
	entries := []map[string]any{}
	for _, sp := range paths[start:end] {
		entries = append(entries, ss.entry(sp))
	}
	res := map[string]any{"resultType": "complete", "skills": entries}
	if end < len(paths) {
		res["nextCursor"] = fmt.Sprint(end)
	}
	return res, nil
}

func (ss *SkillsServer) get(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		URI string `json:"uri"`
	}
	_ = json.Unmarshal(params, &p)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	sp := strings.TrimSuffix(strings.TrimPrefix(p.URI, "skill://"), "/SKILL.md")
	if _, ok := ss.skills[sp]; !ok || !strings.HasSuffix(p.URI, "/SKILL.md") {
		return nil, &jsonrpc.Error{Code: -32602, Message: "unknown skill " + p.URI}
	}
	return map[string]any{"resultType": "complete", "skill": ss.entry(sp)}, nil
}

// TasksServer turns every tools/call result of Server into a task: the
// call returns a task handle, tasks/get reports "working" once, then (when
// Elicit is set) "input_required" with an elicitation until tasks/update
// answers it, then "completed" with the tool's real result.
type TasksServer struct {
	Server *sdk.Server
	// Elicit, when set, is the elicitation the task asks for.
	Elicit *sdk.ElicitParams
	// Fail makes the task end "failed".
	Fail bool
	// Hold keeps the task "working" forever.
	Hold bool

	mu        sync.Mutex
	tasks     map[string]*fakeTask
	next      int
	Responses []json.RawMessage // tasks/update inputResponses, in order
	Cancelled []string
}

type fakeTask struct {
	result   json.RawMessage
	polls    int
	answered bool
}

// NewTasksServer wraps server (which should declare tools).
func NewTasksServer(server *sdk.Server) *TasksServer {
	return &TasksServer{Server: server, tasks: map[string]*fakeTask{}}
}

// Transport wraps a server transport with the tasks extension.
func (ts *TasksServer) Transport(inner sdk.Transport) sdk.Transport {
	return &ExtTransport{
		Inner: inner,
		Methods: map[string]ExtHandler{
			"tasks/get":    ts.get,
			"tasks/update": ts.update,
			"tasks/cancel": ts.cancel,
		},
		RewriteResult: func(method string, result json.RawMessage) json.RawMessage {
			if method != "tools/call" {
				return result
			}
			ts.mu.Lock()
			defer ts.mu.Unlock()
			ts.next++
			id := fmt.Sprintf("task-%d", ts.next)
			ts.tasks[id] = &fakeTask{result: result}
			b, _ := json.Marshal(map[string]any{
				"resultType": "task", "taskId": id, "status": "working",
				"ttlMs": 60000, "pollIntervalMs": 1,
				"createdAt": "2026-01-01T00:00:00Z", "lastUpdatedAt": "2026-01-01T00:00:00Z",
			})
			return b
		},
	}
}

func taskID(params json.RawMessage) string {
	var p struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.TaskID
}

func (ts *TasksServer) get(_ context.Context, params json.RawMessage) (any, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	id := taskID(params)
	t, ok := ts.tasks[id]
	if !ok {
		return nil, &jsonrpc.Error{Code: -32602, Message: "unknown task"}
	}
	t.polls++
	base := map[string]any{"resultType": "complete", "taskId": id, "ttlMs": 60000, "pollIntervalMs": 1,
		"createdAt": "2026-01-01T00:00:00Z", "lastUpdatedAt": "2026-01-01T00:00:00Z"}
	switch {
	case t.polls == 1 || ts.Hold:
		base["status"] = "working"
	case ts.Elicit != nil && !t.answered:
		base["status"] = "input_required"
		base["inputRequests"] = map[string]any{"confirm": map[string]any{"method": "elicitation/create", "params": ts.Elicit}}
	case ts.Fail:
		base["status"] = "failed"
		base["error"] = map[string]any{"code": -32000, "message": "boom"}
	default:
		base["status"] = "completed"
		base["result"] = t.result
	}
	return base, nil
}

func (ts *TasksServer) update(_ context.Context, params json.RawMessage) (any, error) {
	var p struct {
		TaskID         string          `json:"taskId"`
		InputResponses json.RawMessage `json:"inputResponses"`
	}
	_ = json.Unmarshal(params, &p)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if t, ok := ts.tasks[p.TaskID]; ok {
		t.answered = true
	}
	ts.Responses = append(ts.Responses, p.InputResponses)
	return map[string]any{"resultType": "complete"}, nil
}

func (ts *TasksServer) cancel(_ context.Context, params json.RawMessage) (any, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.Cancelled = append(ts.Cancelled, taskID(params))
	return map[string]any{"resultType": "complete"}, nil
}

// Snapshot returns the recorded tasks/update responses and cancellations.
func (ts *TasksServer) Snapshot() (responses []json.RawMessage, cancelled []string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]json.RawMessage(nil), ts.Responses...), append([]string(nil), ts.Cancelled...)
}
