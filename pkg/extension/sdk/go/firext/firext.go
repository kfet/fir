// Package firext is a minimal, idiomatic Go SDK for writing fir extensions.
//
// A fir extension is any executable that speaks the fir JSON-RPC 2.0 protocol
// over stdio (one JSON object per line). This package implements that protocol
// so authors only write handlers.
//
// Example:
//
//	func main() {
//	    app := firext.New("hello-go")
//
//	    app.Tool(firext.ToolSpec{
//	        Name:        "greet",
//	        Description: "Greet someone by name",
//	        Parameters: firext.Object(firext.Props{
//	            "name": firext.Str("Who to greet"),
//	        }, "name"),
//	    }, func(p json.RawMessage, ctx *firext.Context) (*firext.ToolResult, error) {
//	        var in struct{ Name string `json:"name"` }
//	        _ = json.Unmarshal(p, &in)
//	        return firext.Text("Hello, " + in.Name + "!"), nil
//	    })
//
//	    app.On("session_start", func(p json.RawMessage, ctx *firext.Context) {
//	        _ = ctx.SetStatus("hello-go ready")
//	    })
//
//	    app.Run()
//	}
//
// The protocol is specified in docs/extension-protocol.md. This SDK covers the
// common surface: the init handshake, tools, slash commands, events, hooks, and
// the most-used extension→fir callbacks (notify, exec, set_status, side_query,
// send_user_message, send_message, put_observable, get/set_session_data). The
// remaining callbacks can be issued directly via Context.Call.
package firext

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcMessage is the union of request, response and notification shapes.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Public content / spec types
// ---------------------------------------------------------------------------

// ContentBlock is a single block of tool output. Only the "text" type is used.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is what a tool handler returns.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error,omitempty"`
}

// Text builds a successful single-block text result.
func Text(s string) *ToolResult {
	return &ToolResult{Content: []ContentBlock{{Type: "text", Text: s}}}
}

// ErrorText builds an error result (reported to the LLM as a tool error).
func ErrorText(s string) *ToolResult {
	return &ToolResult{Content: []ContentBlock{{Type: "text", Text: s}}, IsError: true}
}

// TitleArgSpec controls how a tool parameter appears on the collapsed header.
type TitleArgSpec struct {
	Name  string `json:"name"`
	Style string `json:"style,omitempty"` // "path"|"pattern"|"accent"|""
	Label string `json:"label,omitempty"`
}

// DisplayHint carries TUI rendering hints for a tool.
type DisplayHint struct {
	TitleArgs      []TitleArgSpec `json:"title_args,omitempty"`
	ResultMaxLines int            `json:"result_max_lines,omitempty"`
	UseBox         bool           `json:"use_box,omitempty"`
}

// ToolSpec declares a tool registered with the LLM.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	DisplayHint *DisplayHint   `json:"display_hint,omitempty"`
	// Timeout is the host-side tool_call timeout in seconds. 0/absent uses
	// fir's default (30s, overridable via FIR_EXT_TOOL_TIMEOUT); > 0 sets an
	// explicit bound; < 0 disables the host-side timeout (the call is then
	// bounded only by the turn context). The wait is activity-aware, so it
	// only clips a call that goes silent. If the tool body waits on a nested
	// ctx.CallTool(..., timeout=T), keep T <= this value.
	Timeout float64 `json:"timeout,omitempty"`
}

// CommandSpec declares a slash command.
type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CommandResult is what a command handler may return. A nil result shows nothing.
type CommandResult struct {
	Message       string `json:"message,omitempty"`
	PrintResponse bool   `json:"print_response,omitempty"`
	Markdown      bool   `json:"markdown,omitempty"`
}

// HookDecision is returned by a hook handler. Return nil to allow.
type HookDecision struct {
	Block  bool   `json:"block,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ExecResult is the result of Context.Exec.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// ---------------------------------------------------------------------------
// JSON-Schema helpers (so authors don't hand-build map[string]any)
// ---------------------------------------------------------------------------

// Props is a convenience alias for a property map.
type Props map[string]any

// Str builds a {"type":"string","description":desc} schema fragment.
func Str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// Int builds a {"type":"integer","description":desc} schema fragment.
func Int(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// Bool builds a {"type":"boolean","description":desc} schema fragment.
func Bool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// Object builds an object schema with the given properties and required keys.
func Object(props Props, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": map[string]any(props)}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ---------------------------------------------------------------------------
// Handler signatures
// ---------------------------------------------------------------------------

// ToolHandler handles a tool call. Return a ToolResult, or an error to send a
// JSON-RPC error back to fir.
type ToolHandler func(params json.RawMessage, ctx *Context) (*ToolResult, error)

// EventHandler handles a notification event (no response).
type EventHandler func(params json.RawMessage, ctx *Context)

// HookHandler intercepts a hook. Return nil to allow; return a *HookDecision
// (for tool_call) to block.
type HookHandler func(params json.RawMessage, ctx *Context) (*HookDecision, error)

// CommandHandler handles a slash command.
type CommandHandler func(args []string, ctx *Context) (*CommandResult, error)

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

// App is an extension instance. Construct with New, register handlers, then Run.
type App struct {
	name string

	tools     map[string]ToolHandler
	toolSpecs []ToolSpec
	commands  map[string]CommandHandler
	cmdSpecs  []CommandSpec
	events    map[string]EventHandler
	hooks     map[string]HookHandler // key: bare hook name, e.g. "tool_call"

	in  io.Reader
	out io.Writer

	writeMu sync.Mutex
	idMu    sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMessage
	pendMu  sync.Mutex

	ctx *Context
}

// New creates an extension named name.
func New(name string) *App {
	a := &App{
		name:     name,
		tools:    map[string]ToolHandler{},
		commands: map[string]CommandHandler{},
		events:   map[string]EventHandler{},
		hooks:    map[string]HookHandler{},
		in:       os.Stdin,
		out:      os.Stdout,
		pending:  map[int64]chan rpcMessage{},
	}
	a.ctx = &Context{app: a}
	return a
}

// Tool registers a tool and its handler.
func (a *App) Tool(spec ToolSpec, h ToolHandler) {
	if spec.Parameters == nil {
		spec.Parameters = Object(Props{})
	}
	a.tools[spec.Name] = h
	a.toolSpecs = append(a.toolSpecs, spec)
}

// Command registers a slash command and its handler.
func (a *App) Command(name, description string, h CommandHandler) {
	a.commands[name] = h
	a.cmdSpecs = append(a.cmdSpecs, CommandSpec{Name: name, Description: description})
}

// On subscribes to an event (e.g. "session_start", "message_end").
func (a *App) On(event string, h EventHandler) {
	a.events[event] = h
}

// Hook registers a hook handler. name is the bare hook name, e.g. "tool_call"
// or "command"; the SDK subscribes it as "hook/<name>".
func (a *App) Hook(name string, h HookHandler) {
	a.hooks[strings.TrimPrefix(name, "hook/")] = h
}

// ---------------------------------------------------------------------------
// Run loop
// ---------------------------------------------------------------------------

// Run starts the protocol loop and blocks until stdin closes. Inbound requests
// and notifications are dispatched in their own goroutines so handlers may make
// outbound calls (which the same loop must receive responses for) without
// deadlocking. Writes are serialized.
func (a *App) Run() error {
	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Unparseable line: log to stderr (forwarded to fir's log).
			fmt.Fprintf(os.Stderr, "firext: bad inbound line: %v\n", err)
			continue
		}
		if msg.Method == "" && msg.ID != nil {
			// Response to one of our outbound calls.
			a.deliver(msg)
			continue
		}
		// Inbound request or notification — handle concurrently.
		go a.handle(msg)
	}
	return scanner.Err()
}

func (a *App) deliver(msg rpcMessage) {
	a.pendMu.Lock()
	ch, ok := a.pending[*msg.ID]
	if ok {
		delete(a.pending, *msg.ID)
	}
	a.pendMu.Unlock()
	if ok {
		ch <- msg
	}
}

func (a *App) handle(msg rpcMessage) {
	switch {
	case msg.Method == "init":
		a.reply(msg.ID, a.initResult(), nil)

	case msg.Method == "tool_call":
		a.handleToolCall(msg)

	case strings.HasPrefix(msg.Method, "hook/"):
		a.handleHook(msg)

	case strings.HasPrefix(msg.Method, "event/"):
		name := strings.TrimPrefix(msg.Method, "event/")
		if h, ok := a.events[name]; ok {
			h(msg.Params, a.ctx)
		}

	default:
		// Unknown request → method-not-found; unknown notification → ignore.
		if msg.ID != nil {
			a.reply(msg.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + msg.Method})
		}
	}
}

func (a *App) initResult() map[string]any {
	events := make([]string, 0, len(a.events)+len(a.hooks))
	for e := range a.events {
		events = append(events, e)
	}
	for h := range a.hooks {
		events = append(events, "hook/"+h)
	}
	specs := a.toolSpecs
	if specs == nil {
		specs = []ToolSpec{}
	}
	cmds := a.cmdSpecs
	if cmds == nil {
		cmds = []CommandSpec{}
	}
	return map[string]any{
		"name":     a.name,
		"tools":    specs,
		"commands": cmds,
		"events":   events,
	}
}

func (a *App) handleToolCall(msg rpcMessage) {
	var p struct {
		Name   string          `json:"name"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	h, ok := a.tools[p.Name]
	if !ok {
		a.reply(msg.ID, nil, &rpcError{Code: -32601, Message: "unknown tool: " + p.Name})
		return
	}
	res, err := h(p.Params, a.ctx)
	if err != nil {
		a.reply(msg.ID, nil, &rpcError{Code: -32000, Message: err.Error()})
		return
	}
	if res == nil {
		res = Text("")
	}
	a.reply(msg.ID, res, nil)
}

func (a *App) handleHook(msg rpcMessage) {
	name := strings.TrimPrefix(msg.Method, "hook/")

	// hook/command is special: it routes to a registered command handler.
	if name == "command" {
		a.handleCommandHook(msg)
		return
	}

	h, ok := a.hooks[name]
	if !ok {
		a.reply(msg.ID, nil, nil) // allow by default
		return
	}
	dec, err := h(msg.Params, a.ctx)
	if err != nil {
		a.reply(msg.ID, nil, &rpcError{Code: -32000, Message: err.Error()})
		return
	}
	if dec == nil {
		a.reply(msg.ID, nil, nil)
		return
	}
	a.reply(msg.ID, dec, nil)
}

func (a *App) handleCommandHook(msg rpcMessage) {
	var p struct {
		Name string   `json:"name"`
		Args []string `json:"args"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	h, ok := a.commands[p.Name]
	if !ok {
		a.reply(msg.ID, nil, nil)
		return
	}
	res, err := h(p.Args, a.ctx)
	if err != nil {
		a.reply(msg.ID, nil, &rpcError{Code: -32000, Message: err.Error()})
		return
	}
	if res == nil {
		a.reply(msg.ID, nil, nil)
		return
	}
	a.reply(msg.ID, res, nil)
}

// ---------------------------------------------------------------------------
// Outbound writes
// ---------------------------------------------------------------------------

func (a *App) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if _, err := a.out.Write(data); err != nil {
		return err
	}
	_, err = a.out.Write([]byte("\n"))
	return err
}

func (a *App) reply(id *int64, result any, rerr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	if err := a.writeMessage(resp); err != nil {
		fmt.Fprintf(os.Stderr, "firext: write reply: %v\n", err)
	}
}

func (a *App) allocID() int64 {
	a.idMu.Lock()
	defer a.idMu.Unlock()
	a.nextID++
	return a.nextID
}

// call issues an outbound request and waits for the matching response.
func (a *App) call(method string, params any) (json.RawMessage, error) {
	id := a.allocID()
	ch := make(chan rpcMessage, 1)
	a.pendMu.Lock()
	a.pending[id] = ch
	a.pendMu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := a.writeMessage(req); err != nil {
		a.pendMu.Lock()
		delete(a.pending, id)
		a.pendMu.Unlock()
		return nil, err
	}

	resp := <-ch
	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// ---------------------------------------------------------------------------
// Context — extension → fir callbacks
// ---------------------------------------------------------------------------

// Context is passed to every handler and provides callbacks into fir.
type Context struct {
	app *App
}

// Call issues an arbitrary outbound request and returns the raw result. Use the
// typed helpers below for common methods; this is the escape hatch.
func (c *Context) Call(method string, params any) (json.RawMessage, error) {
	return c.app.call(method, params)
}

// Notify shows a notification. level is "info", "warning" or "error".
func (c *Context) Notify(message, level string) error {
	if level == "" {
		level = "info"
	}
	_, err := c.app.call("notify", map[string]any{"message": message, "level": level})
	return err
}

// Exec runs a subprocess via fir and returns its output.
func (c *Context) Exec(command string, args ...string) (*ExecResult, error) {
	raw, err := c.app.call("exec", map[string]any{"command": command, "args": args})
	if err != nil {
		return nil, err
	}
	var res ExecResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SetStatus sets the footer status text. Empty string clears it.
func (c *Context) SetStatus(status string) error {
	_, err := c.app.call("set_status", map[string]any{"status": status})
	return err
}

// SetSessionName sets the session display name.
func (c *Context) SetSessionName(name string) error {
	_, err := c.app.call("set_session_name", map[string]any{"name": name})
	return err
}

// SendUserMessage injects a user-role message. deliverAs is "steer",
// "followUp", or "" (default: triggers a new prompt turn).
func (c *Context) SendUserMessage(content, deliverAs string) error {
	params := map[string]any{"content": content}
	if deliverAs != "" {
		params["deliver_as"] = deliverAs
	}
	_, err := c.app.call("send_user_message", params)
	return err
}

// SendMessage injects a custom-typed message into the session log.
func (c *Context) SendMessage(customType string, content any, display bool) error {
	_, err := c.app.call("send_message", map[string]any{
		"custom_type": customType,
		"content":     content,
		"display":     display,
	})
	return err
}

// PutObservable publishes an observable card under this extension's source.
func (c *Context) PutObservable(key, slug, detail string) error {
	_, err := c.app.call("put_observable", map[string]any{
		"key": key, "slug": slug, "detail": detail,
	})
	return err
}

// SideQuery runs an ephemeral side-query LLM call using the current session
// context and returns the response text. timeoutSec <= 0 uses fir's default.
func (c *Context) SideQuery(question string, timeoutSec int) (string, error) {
	params := map[string]any{"question": question}
	if timeoutSec > 0 {
		params["timeout"] = timeoutSec
	}
	raw, err := c.app.call("side_query", params)
	if err != nil {
		return "", err
	}
	// Result may be a bare string or {"response": "..."} depending on fir
	// version; accept both.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var obj struct {
		Response string `json:"response"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	if obj.Response != "" {
		return obj.Response, nil
	}
	return obj.Text, nil
}

// AvailableModel is one entry returned by Context.AvailableModels.
type AvailableModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

// AvailableModels returns the models the session currently considers live
// and authed (the host's available_models bridge verb → registry
// GetAvailable()). Tolerates older hosts: on any RPC error it returns an
// empty slice and nil error so callers degrade to static configuration.
func (c *Context) AvailableModels() ([]AvailableModel, error) {
	raw, err := c.app.call("available_models", nil)
	if err != nil {
		return nil, nil
	}
	var obj struct {
		Models []AvailableModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil
	}
	return obj.Models, nil
}

// SetSessionData stores a key/value pair persisted across /reexec.
func (c *Context) SetSessionData(key string, value any) error {
	_, err := c.app.call("set_session_data", map[string]any{"key": key, "value": value})
	return err
}

// GetSessionData retrieves a previously stored value as raw JSON.
func (c *Context) GetSessionData(key string) (json.RawMessage, error) {
	raw, err := c.app.call("get_session_data", map[string]any{"key": key})
	if err != nil {
		return nil, err
	}
	// fir returns {"value": <any>}; unwrap.
	var obj struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, nil
	}
	return obj.Value, nil
}

// GetSessionID returns the current session's unique identifier.
func (c *Context) GetSessionID() (string, error) {
	raw, err := c.app.call("get_session_id", nil)
	if err != nil {
		return "", err
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var obj struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.SessionID, nil
}
