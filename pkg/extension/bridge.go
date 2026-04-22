package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
)

// NotifyFunc is called when an extension sends a "notify" request.
type NotifyFunc func(level, message string)

// SetStatusFunc is called when an extension sends a "set_status" request.
// name is the extension name; status is the text to display (empty = clear).
type SetStatusFunc func(name, status string)

type Bridge struct {
	proc *Process
	caps *InitResult

	// subscribedEvents is a set for fast lookup.
	subscribedEvents map[string]bool

	// NotifyFn is called for inbound "notify" requests. If nil, an error is returned.
	NotifyFn NotifyFunc

	// SetStatusFn is called for inbound "set_status" requests. If nil, an error is returned.
	SetStatusFn SetStatusFunc

	// nextID generates unique request IDs for outbound requests.
	// Starts at 100 to avoid collision with handshake ID (1).
	nextID atomic.Int64

	// pending tracks outbound requests waiting for a response.
	pendingMu sync.Mutex
	pending   map[int64]chan *Response

	// sessionData is a per-extension key/value store persisted across /reexec.
	sessionDataMu sync.RWMutex
	sessionData   map[string]string

	// authCallbacks holds the active login callbacks for UI dispatch during auth/login.
	authCallbacksMu sync.RWMutex
	authCallbacks   *oauth.LoginCallbacks

	// authProviders holds the extAuthProvider instances for this bridge.
	authProvidersMu sync.RWMutex
	authProviders   []*extAuthProvider

	// lastActivity tracks the last time the extension sent us any message
	// (request or response). Used by CallHook to extend timeouts when the
	// extension is still alive but busy (e.g. aside making call_tool calls).
	lastActivity atomic.Int64 // UnixNano

	// activeCtx holds the context of the single in-flight tool_call.
	// Set by the Execute closure before CallHook, cleared after.
	// Only one tool_call is ever in-flight per extension (agent loop is serial).
	activeCtx            atomic.Pointer[context.Context]
	activeReportProgress atomic.Pointer[func(string)]
}

// NewBridge creates a Bridge wrapping the given Process and its capabilities.
func NewBridge(proc *Process, caps *InitResult) *Bridge {
	events := make(map[string]bool, len(caps.Events))
	for _, e := range caps.Events {
		events[e] = true
	}
	b := &Bridge{
		proc:             proc,
		caps:             caps,
		subscribedEvents: events,
		pending:          make(map[int64]chan *Response),
	}
	b.nextID.Store(100) // avoid collision with handshake ID
	return b
}

// Run starts the dispatch loop, reading messages from the process and routing
// them. It blocks until ctx is cancelled or the codec returns an error.
func (b *Bridge) Run(ctx context.Context, api BridgeAPI) error {
	codec := b.proc.GetCodec()
	if codec == nil {
		return fmt.Errorf("extension: process not started")
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			msg, err := codec.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			switch m := msg.(type) {
			case *Request:
				b.lastActivity.Store(time.Now().UnixNano())
				go b.handleInbound(m, codec, api)
			case *Response:
				b.lastActivity.Store(time.Now().UnixNano())
				b.routeResponse(m)
			case *Notification:
				b.lastActivity.Store(time.Now().UnixNano())
				b.handleNotification(m, api)
			}
		}
	}()

	select {
	case <-ctx.Done():
		// Close stdin to unblock the reader goroutine.
		b.proc.CloseStdin()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// handleInbound dispatches an inbound request from the extension to the API.
func (b *Bridge) handleInbound(req *Request, codec *Codec, api BridgeAPI) {
	var result any
	var rpcErr *Error

	switch req.Method {
	case "notify":
		var p struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if b.NotifyFn != nil {
			b.NotifyFn(p.Level, p.Message)
		}
		result = map[string]any{"ok": true}

	case "exec":
		var p struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		r, err := api.Exec(p.Command, p.Args)
		if err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = r
		}

	case "send_message":
		var p struct {
			CustomType  string `json:"custom_type"`
			Content     any    `json:"content"`
			Display     bool   `json:"display"`
			DeliverAs   string `json:"deliver_as"`
			TriggerTurn bool   `json:"trigger_turn"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SendMessage(CustomMessageSpec{
			CustomType: p.CustomType,
			Content:    p.Content,
			Display:    p.Display,
		}, &SendMessageOptions{
			DeliverAs:   p.DeliverAs,
			TriggerTurn: p.TriggerTurn,
		})
		result = map[string]any{"ok": true}

	case "send_user_message":
		var p struct {
			Content   string `json:"content"`
			DeliverAs string `json:"deliver_as"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SendUserMessage(p.Content, &SendUserMessageOptions{
			DeliverAs: p.DeliverAs,
		})
		result = map[string]any{"ok": true}

	case "set_session_name":
		var p struct {
			Name string `json:"name"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SetSessionName(p.Name)
		result = map[string]any{"ok": true}

	case "set_label":
		var p struct {
			EntryID string `json:"entry_id"`
			Label   string `json:"label"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SetLabel(p.EntryID, p.Label)
		result = map[string]any{"ok": true}

	case "clear_label":
		var p struct {
			EntryID string `json:"entry_id"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.ClearLabel(p.EntryID)
		result = map[string]any{"ok": true}

	case "set_model":
		var p struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		ok := api.SetModel(&ai.Model{Provider: p.Provider, ID: p.ID})
		result = map[string]any{"ok": ok}

	case "set_status":
		var p struct {
			Status string `json:"status"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if b.SetStatusFn != nil {
			b.SetStatusFn(b.caps.Name, p.Status)
		}
		result = map[string]any{"ok": true}

	case "continue_session":
		if err := api.ContinueSession(); err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = map[string]any{"ok": true}
		}

	case "side_query":
		var p struct {
			Question string `json:"question"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		stop := b.keepAlive()
		text, err := api.SideQuery(p.Question)
		stop()
		if err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = map[string]any{"ok": true, "text": text}
		}

	case "set_session_data":
		var p struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		b.SetSessionData(p.Key, p.Value)
		result = map[string]any{"ok": true}

	case "get_session_data":
		var p struct {
			Key string `json:"key"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		value, ok := b.GetSessionData(p.Key)
		result = map[string]any{"value": value, "ok": ok}

	case "call_tool":
		var p struct {
			Name   string         `json:"name"`
			Params map[string]any `json:"params"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		stop := b.keepAlive()
		ctx := context.Background()
		if active := b.activeCtx.Load(); active != nil {
			ctx = *active
		}
		r, err := api.CallTool(ctx, p.Name, p.Params)
		stop()
		if err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = r
		}

	case "list_tools":
		result = api.ListTools()

	case "prepend_context":
		var p struct {
			Content string `json:"content"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.PrependContext(p.Content)
		result = map[string]any{"ok": true}

	case "report_progress":
		var p struct {
			Message string `json:"message"`
		}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		b.reportProgress(p.Message, api)
		result = map[string]any{"ok": true}

	case "agent.info":
		result = api.Introspect()

	default:
		// Try auth helper RPCs.
		if result, rpcErr, handled := b.handleAuthHelperRPC(req.Method, req.Params); handled {
			_ = codec.WriteResponse(req.ID, result, rpcErr)
			return
		}
		rpcErr = &Error{Code: -32601, Message: "method not found: " + req.Method}
	}

	_ = codec.WriteResponse(req.ID, result, rpcErr)
}

// handleNotification processes a JSON-RPC notification (no response expected).
// Currently only report_progress is handled; other notifications are ignored.
// reportProgress dispatches a progress message to the active tool call's
// reporter, falling back to api.ReportProgress if none is active.
func (b *Bridge) reportProgress(message string, api BridgeAPI) {
	if fn := b.activeReportProgress.Load(); fn != nil {
		(*fn)(message)
	} else {
		api.ReportProgress(message)
	}
}

func (b *Bridge) handleNotification(n *Notification, api BridgeAPI) {
	switch n.Method {
	case "report_progress":
		var p struct {
			Message string `json:"message"`
		}
		if n.Params != nil {
			_ = json.Unmarshal(*n.Params, &p)
		}
		b.reportProgress(p.Message, api)
	}
}

// routeResponse delivers an inbound response to the waiting caller.
func (b *Bridge) routeResponse(resp *Response) {
	var id int64
	switch v := resp.ID.(type) {
	case float64:
		id = int64(v)
	case int64:
		id = v
	case json.Number:
		n, _ := v.Int64()
		id = n
	default:
		return
	}

	b.pendingMu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.pendingMu.Unlock()
	if ok {
		ch <- resp
	}
}

// keepAliveInterval is the ticker interval for keepAlive. Exported for testing.
var keepAliveInterval = 5 * time.Second

// keepAlive starts a background goroutine that periodically updates
// lastActivity while a long-running bridge call (side_query, call_tool)
// is in progress. Returns a stop function that must be called when done.
func (b *Bridge) keepAlive() func() {
	ticker := time.NewTicker(keepAliveInterval)
	done := make(chan struct{})
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.lastActivity.Store(time.Now().UnixNano())
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}

// EmitEvent sends a JSON-RPC notification to the extension if it subscribed.
func (b *Bridge) EmitEvent(name string, data any) error {
	if !b.subscribedEvents[name] {
		return nil
	}
	codec := b.proc.GetCodec()
	if codec == nil {
		return fmt.Errorf("extension: not connected")
	}
	return codec.WriteNotification("event/"+name, data)
}

// CallHook sends a JSON-RPC request and waits for a response with timeout.
func (b *Bridge) CallHook(ctx context.Context, name string, data any, timeout time.Duration) (json.RawMessage, error) {
	codec := b.proc.GetCodec()
	if codec == nil {
		return nil, fmt.Errorf("extension: not connected")
	}

	id := b.nextID.Add(1)
	ch := make(chan *Response, 1)

	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()

	if err := codec.WriteRequest(int(id), name, data); err != nil {
		b.pendingMu.Lock()
		delete(b.pending, id)
		b.pendingMu.Unlock()
		return nil, err
	}

	// Use an activity-aware timeout: reset the deadline whenever the
	// extension sends us any message (request or response), which means
	// it's still alive and working (e.g. aside making call_tool calls).
	b.lastActivity.Store(time.Now().UnixNano())
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case resp := <-ch:
			if resp.Error != nil {
				return nil, resp.Error
			}
			if resp.Result != nil {
				return *resp.Result, nil
			}
			return nil, nil
		case <-ctx.Done():
			b.pendingMu.Lock()
			delete(b.pending, id)
			b.pendingMu.Unlock()
			return nil, ctx.Err()
		case <-deadline.C:
			// Check if there was recent activity before giving up.
			since := time.Since(time.Unix(0, b.lastActivity.Load()))
			if since < timeout {
				// Extension is still active — extend the deadline.
				deadline.Reset(timeout - since)
				continue
			}
			b.pendingMu.Lock()
			delete(b.pending, id)
			b.pendingMu.Unlock()
			return nil, fmt.Errorf("extension: hook %s timed out after %s", name, timeout)
		}
	}
}

// RegisterTools registers each tool from InitResult on the given API.
func (b *Bridge) RegisterTools(api BridgeAPI) {
	for _, t := range b.caps.Tools {
		tool := t // capture
		api.RegisterTool(ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			DisplayHint: tool.DisplayHint,
			Bridge:      b,
			Execute: func(ctx ToolContext) (ToolResult, error) {
				// Store context so inbound call_tool requests use it.
				b.activeCtx.Store(&ctx.Context)
				defer b.activeCtx.Store(nil)

				params := map[string]any{
					"tool_call_id": ctx.ToolCallID,
					"name":         tool.Name,
					"params":       ctx.Params,
				}
				raw, err := b.CallHook(ctx.Context, "tool_call", params, 30*time.Second)
				if err != nil {
					return ToolResult{
						Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: err.Error()}},
						IsError: true,
					}, nil
				}
				var result ToolResult
				if raw != nil {
					if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
						// Result is not in structured format — wrap raw JSON as text.
						// Also handles plain strings like "hello".
						var text string
						if json.Unmarshal(raw, &text) == nil {
							result.Content = []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: text}}
						} else {
							result.Content = []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: string(raw)}}
						}
					}
				}
				// Ensure all content blocks have a type set.
				for i := range result.Content {
					if result.Content[i].Type == "" && result.Content[i].Text != "" {
						result.Content[i].Type = ai.ContentTypeText
					}
				}
				return result, nil
			},
		})
	}
}

// ---------------------------------------------------------------------------
// Per-extension session data (persisted across /reexec via sidecar)
// ---------------------------------------------------------------------------

// SetSessionData stores key→value in this bridge's session data map.
// Called by the BridgeAPI implementation when the extension calls set_session_data.
func (b *Bridge) SetSessionData(key, value string) {
	b.sessionDataMu.Lock()
	defer b.sessionDataMu.Unlock()
	if b.sessionData == nil {
		b.sessionData = make(map[string]string)
	}
	b.sessionData[key] = value
}

// GetSessionData retrieves a value from this bridge's session data map.
func (b *Bridge) GetSessionData(key string) (string, bool) {
	b.sessionDataMu.RLock()
	defer b.sessionDataMu.RUnlock()
	v, ok := b.sessionData[key]
	return v, ok
}

// GetAllSessionData returns a snapshot of this bridge's entire session data map.
func (b *Bridge) GetAllSessionData() map[string]string {
	b.sessionDataMu.RLock()
	defer b.sessionDataMu.RUnlock()
	if len(b.sessionData) == 0 {
		return nil
	}
	out := make(map[string]string, len(b.sessionData))
	for k, v := range b.sessionData {
		out[k] = v
	}
	return out
}

// SeedSessionData pre-populates session data (called on startup with reexec sidecar data).
func (b *Bridge) SeedSessionData(data map[string]string) {
	if len(data) == 0 {
		return
	}
	b.sessionDataMu.Lock()
	defer b.sessionDataMu.Unlock()
	if b.sessionData == nil {
		b.sessionData = make(map[string]string, len(data))
	}
	for k, v := range data {
		b.sessionData[k] = v
	}
}
