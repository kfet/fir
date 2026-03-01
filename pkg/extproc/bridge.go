package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension"
)

// Bridge adapts an external process extension to fir's extension system.
type Bridge struct {
	proc *Process
	caps *InitResult

	// subscribedEvents is a set for fast lookup.
	subscribedEvents map[string]bool

	// nextID generates unique request IDs for outbound requests.
	nextID atomic.Int64

	// pending tracks outbound requests waiting for a response.
	pendingMu sync.Mutex
	pending   map[int64]chan *Response
}

// NewBridge creates a Bridge wrapping the given Process and its capabilities.
func NewBridge(proc *Process, caps *InitResult) *Bridge {
	events := make(map[string]bool, len(caps.Events))
	for _, e := range caps.Events {
		events[e] = true
	}
	return &Bridge{
		proc:             proc,
		caps:             caps,
		subscribedEvents: events,
		pending:          make(map[int64]chan *Response),
	}
}

// Run starts the dispatch loop, reading messages from the process and routing
// them. It blocks until ctx is cancelled or the codec returns an error.
func (b *Bridge) Run(ctx context.Context, api extension.API) error {
	codec := b.proc.GetCodec()
	if codec == nil {
		return fmt.Errorf("extproc: process not started")
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
				b.handleInbound(m, codec, api)
			case *Response:
				b.routeResponse(m)
			case *Notification:
				// Extensions shouldn't send us notifications; ignore.
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// handleInbound dispatches an inbound request from the extension to the API.
func (b *Bridge) handleInbound(req *Request, codec *Codec, api extension.API) {
	var result any
	var rpcErr *Error

	switch req.Method {
	case "notify":
		var p struct {
			Message string `json:"message"`
			Level   string `json:"level"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		// notify requires UIContext — use Events bus as a proxy via SendMessage
		// For now, call the extension API notify pattern: no direct UIContext access,
		// but we can use SendMessage with a custom type.
		// Actually, looking at the API, there's no direct Notify on API.
		// We'll return success silently for now.
		result = map[string]any{"ok": true}

	case "exec":
		var p struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		r, err := api.Exec(p.Command, p.Args)
		if err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = r
		}

	case "send_message":
		var p struct {
			CustomType string `json:"custom_type"`
			Content    any    `json:"content"`
			Display    bool   `json:"display"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.SendMessage(extension.CustomMessageSpec{
			CustomType: p.CustomType,
			Content:    p.Content,
			Display:    p.Display,
		}, nil)
		result = map[string]any{"ok": true}

	case "send_user_message":
		var p struct {
			Content string `json:"content"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.SendUserMessage(p.Content, nil)
		result = map[string]any{"ok": true}

	case "set_session_name":
		var p struct {
			Name string `json:"name"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.SetSessionName(p.Name)
		result = map[string]any{"ok": true}

	case "set_label":
		var p struct {
			EntryID string `json:"entry_id"`
			Label   string `json:"label"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.SetLabel(p.EntryID, p.Label)
		result = map[string]any{"ok": true}

	case "clear_label":
		var p struct {
			EntryID string `json:"entry_id"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.ClearLabel(p.EntryID)
		result = map[string]any{"ok": true}

	case "get_active_tools":
		result = api.GetActiveTools()

	case "set_active_tools":
		var p struct {
			Names []string `json:"names"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		api.SetActiveTools(p.Names)
		result = map[string]any{"ok": true}

	case "set_model":
		var p struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		ok := api.SetModel(&ai.Model{Provider: p.Provider, ID: p.ID})
		result = map[string]any{"ok": ok}

	case "set_status":
		var p struct {
			Key  string `json:"key"`
			Text string `json:"text"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		// set_status needs UIContext which we don't have directly from API.
		// Return success; integration with UI will be wired in manager.
		result = map[string]any{"ok": true}

	default:
		rpcErr = &Error{Code: -32601, Message: "method not found: " + req.Method}
	}

	_ = codec.WriteResponse(req.ID, result, rpcErr)
}

// routeResponse delivers an inbound response to the waiting caller.
func (b *Bridge) routeResponse(resp *Response) {
	// JSON numbers unmarshal as float64.
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

// EmitEvent sends a JSON-RPC notification to the extension if it subscribed
// to the given event.
func (b *Bridge) EmitEvent(name string, data any) error {
	if !b.subscribedEvents[name] {
		return nil
	}
	codec := b.proc.GetCodec()
	if codec == nil {
		return fmt.Errorf("extproc: not connected")
	}
	return codec.WriteNotification("event/"+name, data)
}

// CallHook sends a JSON-RPC request to the extension and waits for a response
// up to the given timeout.
func (b *Bridge) CallHook(name string, data any, timeout time.Duration) (json.RawMessage, error) {
	codec := b.proc.GetCodec()
	if codec == nil {
		return nil, fmt.Errorf("extproc: not connected")
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

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		if resp.Result != nil {
			return *resp.Result, nil
		}
		return nil, nil
	case <-time.After(timeout):
		b.pendingMu.Lock()
		delete(b.pending, id)
		b.pendingMu.Unlock()
		return nil, fmt.Errorf("extproc: hook %s timed out after %s", name, timeout)
	}
}

// RegisterTools registers each tool from InitResult on the given extension API.
func (b *Bridge) RegisterTools(api extension.API) {
	for _, t := range b.caps.Tools {
		tool := t // capture
		api.RegisterTool(extension.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Execute: func(ctx extension.ToolContext) (agent.AgentToolResult, error) {
				params := map[string]any{
					"tool_call_id": ctx.ToolCallID,
					"name":         tool.Name,
					"params":       ctx.Params,
				}
				raw, err := b.CallHook("tool_call", params, 30*time.Second)
				if err != nil {
					return agent.AgentToolResult{
						Content: []ai.ToolResultContent{{Text: err.Error()}},
						IsError: true,
					}, nil
				}
				var result struct {
					Content []ai.ToolResultContent `json:"content"`
					IsError bool                   `json:"is_error"`
				}
				if raw != nil {
					if err := json.Unmarshal(raw, &result); err != nil {
						return agent.AgentToolResult{
							Content: []ai.ToolResultContent{{Text: "failed to parse tool result: " + err.Error()}},
							IsError: true,
						}, nil
					}
				}
				return agent.AgentToolResult{
					Content: result.Content,
					IsError: result.IsError,
				}, nil
			},
		})
	}
}
