package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/pinoauth"
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

	// Inbound-callback hooks. Manipulated via atomic.Pointer so the
	// Manager can hot-swap them from any goroutine after the bridge has
	// already been started (interactive mode constructs these against UI
	// state that only exists after TUI init, which races with the
	// bridge's worker goroutine reading them). Use the *Fn() getters and
	// SetNotifyFn / SetSetStatusFn setters; never read the pointer
	// directly.
	notifyFn    atomic.Pointer[NotifyFunc]
	setStatusFn atomic.Pointer[SetStatusFunc]

	// nextID generates unique request IDs for outbound requests.
	// Starts at 100 to avoid collision with handshake ID (1).
	nextID atomic.Int64

	// pending tracks outbound requests waiting for a response.
	// closed is set (with closeErr) by failAllPending when the read loop
	// exits, so a CallHook that starts after close fails fast instead of
	// registering into the fresh map and blocking forever.
	pendingMu sync.Mutex
	pending   map[int64]chan *Response
	closed    bool
	closeErr  error

	// sessionData is a per-extension key/value store persisted across /reexec.
	sessionDataMu sync.RWMutex
	sessionData   map[string]string

	// authCallbacks holds the active login callbacks for UI dispatch during auth/login.
	// authCtx is the active login's context, threaded through to callback-server
	// hooks. Both are guarded by authCallbacksMu and set/cleared together.
	authCallbacksMu sync.RWMutex
	authCallbacks   *pinoauth.LoginCallbacks
	authCtx         context.Context

	// authProviders holds the imperative-flow extAuthProvider instances
	// for this bridge (extensions that implement auth/login themselves).
	// genericAuthProviders holds the declarative-flow providers (Go
	// drives the flow; ext only handles optional hooks).
	authProvidersMu      sync.RWMutex
	authProviders        []*extAuthProvider
	genericAuthProviders []*genericAuthProvider

	// providers holds the hosted-provider registrations contributed by
	// this extension at handshake.
	providersMu sync.RWMutex
	providers   []*extProviderRegistration
	apis        []*extApiRegistration

	// activeStreams maps an in-flight stream ID to the destination
	// AssistantMessageEventStream the bridge is forwarding events into.
	activeStreams sync.Map // string → *ai.AssistantMessageEventStream

	// lastActivity tracks the last time the extension sent us any message
	// (request or response). Used by CallHook to extend timeouts when the
	// extension is still alive but busy (e.g. aside making call_tool calls).
	lastActivity atomic.Int64 // UnixNano

	// activeCtx holds the context of the single in-flight tool_call.
	// Set by the Execute closure before CallHook, cleared after.
	// Only one tool_call is ever in-flight per extension (agent loop is serial).
	activeCtx            atomic.Pointer[context.Context]
	activeReportProgress atomic.Pointer[func(string)]

	// activeToolCallID is the tool_call_id of the in-flight tool call
	// dispatched into this extension. currentEntryID() reads this to
	// stamp Card.EntryID. Nil outside of a tool_call window.
	activeToolCallID atomic.Pointer[string]

	// store is the per-session observable cards store. Nil before
	// SetObservableStore wires it; put/clear/set_status no-op until
	// then.
	store atomic.Pointer[store.ObservableStore]
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

// SetNotifyFn atomically installs (or clears, when fn is nil) the
// inbound-notify callback. Safe to call from any goroutine at any time
// during the bridge lifecycle.
func (b *Bridge) SetNotifyFn(fn NotifyFunc) {
	if fn == nil {
		b.notifyFn.Store(nil)
		return
	}
	b.notifyFn.Store(&fn)
}

// SetSetStatusFn atomically installs (or clears, when fn is nil) the
// inbound-set_status callback. Safe to call from any goroutine at any
// time during the bridge lifecycle.
func (b *Bridge) SetSetStatusFn(fn SetStatusFunc) {
	if fn == nil {
		b.setStatusFn.Store(nil)
		return
	}
	b.setStatusFn.Store(&fn)
}

// notifyFunc returns the current notify callback, or nil if unset.
func (b *Bridge) notifyFunc() NotifyFunc {
	if p := b.notifyFn.Load(); p != nil {
		return *p
	}
	return nil
}

// setStatusFunc returns the current set_status callback, or nil if unset.
func (b *Bridge) setStatusFunc() SetStatusFunc {
	if p := b.setStatusFn.Load(); p != nil {
		return *p
	}
	return nil
}

// SetObservableStore wires the per-session observable cards store into
// this bridge. Inbound put_observable / clear_observable / set_status
// RPCs will route through this store. Safe to call at any point during
// the bridge lifecycle; callers typically set it once just after
// NewBridge from the SessionBridge's session. Passing nil detaches.
func (b *Bridge) SetObservableStore(s *store.ObservableStore) {
	b.store.Store(s)
}

// observableStore returns the current observable store, or nil.
func (b *Bridge) observableStore() *store.ObservableStore {
	return b.store.Load()
}

// currentEntryID returns the in-flight tool_call_id (stamped onto
// observable cards as their EntryID), or "" outside a tool dispatch
// window. See docs/design/observable-cards.md "Provenance".
func (b *Bridge) currentEntryID() string {
	if p := b.activeToolCallID.Load(); p != nil {
		return *p
	}
	return ""
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
		b.failAllPending(ctx.Err())
		return ctx.Err()
	case err := <-errCh:
		// The extension's stream closed (crash / EOF). Fail outstanding
		// CallHook waiters so they don't block — critical for tools with a
		// disabled host-side timeout, which have no deadline of their own.
		b.failAllPending(err)
		return err
	}
}

// handleInbound dispatches an inbound request from the extension to the API.
func (b *Bridge) handleInbound(req *Request, codec *Codec, api BridgeAPI) {
	var result any
	var rpcErr *Error

	switch req.Method {
	case "notify":
		var p notifyParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if fn := b.notifyFunc(); fn != nil {
			fn(p.Level, p.Message)
		}
		result = okTrue

	case "exec":
		var p execParams
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
		var p sendMessageParams
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
		result = okTrue

	case "send_user_message":
		var p sendUserMessageParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SendUserMessage(p.Content, &SendUserMessageOptions{
			DeliverAs: p.DeliverAs,
		})
		result = okTrue

	case "set_session_name":
		var p setSessionNameParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SetSessionName(p.Name)
		result = okTrue

	case "set_label":
		var p setLabelParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.SetLabel(p.EntryID, p.Label)
		result = okTrue

	case "clear_label":
		var p clearLabelParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.ClearLabel(p.EntryID)
		result = okTrue

	case "set_model":
		var p setModelParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		ok := api.SetModel(&ai.Model{Provider: p.Provider, ID: p.ID})
		result = OkResult{Ok: ok}

	case "available_models":
		// No params. Returns the session registry's live-and-authed model
		// set so extensions can adapt routing to runtime availability.
		models := api.GetAvailableModels()
		out := make([]AvailableModel, 0, len(models))
		for _, m := range models {
			if m == nil {
				continue
			}
			out = append(out, AvailableModel{
				Provider: m.Provider,
				ID:       m.ID,
				Name:     m.Name,
			})
		}
		result = AvailableModelsResult{Models: out}

	case "set_status":
		var p setStatusParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		// Canonical write: route through the observable store as the
		// "footer" card under this extension's source. Empty status
		// clears the card so observers don't see a stale empty slug.
		// The UI callback is invoked with the *untruncated* status
		// because the TUI footer renders the full string; only the
		// card slug is truncated host-side (SlugMaxLen).
		if s := b.observableStore(); s != nil {
			if p.Status == "" {
				s.Clear(b.caps.Name, "footer")
			} else {
				s.Put(b.caps.Name, "footer", p.Status, "", b.currentEntryID())
			}
		}
		if fn := b.setStatusFunc(); fn != nil {
			fn(b.caps.Name, p.Status)
		}
		result = okTrue

	case "put_observable":
		var p putObservableParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if p.Key == "" {
			rpcErr = &Error{Code: -32602, Message: "put_observable: key is required"}
			break
		}
		if s := b.observableStore(); s != nil {
			// Trust seam: source and EntryID are stamped here, never
			// read from the payload.
			s.Put(b.caps.Name, p.Key, p.Slug, p.Detail, b.currentEntryID())
		}
		result = okTrue

	case "clear_observable":
		var p clearObservableParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if p.Key == "" {
			rpcErr = &Error{Code: -32602, Message: "clear_observable: key is required"}
			break
		}
		if s := b.observableStore(); s != nil {
			s.Clear(b.caps.Name, p.Key)
		}
		result = okTrue

	case "continue_session":
		if err := api.ContinueSession(); err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = okTrue
		}

	case "side_query":
		var p sideQueryParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		var opts *session.SideQueryOptions
		if p.Model != "" || p.Provider != "" || p.Effort != "" {
			opts = &session.SideQueryOptions{
				Model:    p.Model,
				Provider: p.Provider,
				Effort:   ai.ThinkingLevel(p.Effort),
			}
		}
		stop := b.keepAlive()
		if p.Stream {
			// Streaming flavor — push each delta back to the extension as
			// a "side_query/delta" notification correlated to this
			// request's id, then send the terminating response below.
			reqIDInt := jsonRPCIDAsInt(req.ID)
			var seq int
			onDelta := func(d session.SideQueryDelta) {
				params := SideQueryDeltaParams{
					RequestID: reqIDInt,
					Type:      d.Type,
					Text:      d.Text,
					TokensOut: d.TokensOut,
					Seq:       seq,
				}
				seq++
				// Errors here are unrecoverable from the LLM stream's
				// perspective — the extension will see the missing
				// notifications and either time out or get a final
				// response. We deliberately swallow the write error to
				// keep the LLM stream draining cleanly.
				_ = codec.WriteNotification("side_query/delta", params)
			}
			res, err := api.SideQueryStream(p.Question, opts, onDelta)
			stop()
			if err != nil {
				rpcErr = &Error{Code: -32000, Message: err.Error()}
			} else {
				result = SideQueryResult{
					Ok:           true,
					Text:         res.Text,
					Blocks:       res.Blocks,
					FinishReason: res.FinishReason,
				}
			}
		} else {
			text, err := api.SideQuery(p.Question, opts)
			stop()
			if err != nil {
				rpcErr = &Error{Code: -32000, Message: err.Error()}
			} else {
				result = SideQueryResult{Ok: true, Text: text}
			}
		}

	case "set_session_data":
		var p setSessionDataParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		b.SetSessionData(p.Key, p.Value)
		result = okTrue

	case "get_session_data":
		var p getSessionDataParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		value, ok := b.GetSessionData(p.Key)
		result = GetSessionDataResult{Value: value, Ok: ok}

	case "get_session_file":
		// Returns absolute path to the session's JSONL transcript, or "" for
		// in-memory sessions. Used by extensions that want to expose the
		// transcript to outside readers (e.g. observe.py writes the path
		// into its sidecar so `fir observe` can tail -F it).
		result = GetSessionFileResult{Path: api.GetSessionFile()}

	case "get_session_id":
		// Returns the unique session ID. Also available as "session_id" in
		// the session_start event params, but this method allows retrieval
		// at any point during the session lifetime.
		result = GetSessionIDResult{ID: api.GetSessionID()}

	case "get_session_name":
		// Returns the session's display name, or "" if unset.
		result = GetSessionNameResult{Name: api.GetSessionName()}

	case "call_tool":
		var p callToolParams
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
		var p prependContextParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		api.PrependContext(p.Content)
		result = okTrue

	case "report_progress":
		var p reportProgressParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		b.reportProgress(p.Message, api)
		result = okTrue

	case "agent.info":
		result = api.Introspect()

	case "restart_session":
		var p restartSessionParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if err := api.RestartSession(p.Prompt, p.PrependContext); err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = okTrue
		}

	case "reload_extension":
		var p reloadExtensionParams
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				rpcErr = &Error{Code: -32602, Message: "invalid params: " + err.Error()}
				break
			}
		}
		if p.Name == "" {
			rpcErr = &Error{Code: -32602, Message: "reload_extension: name is required"}
			break
		}
		// Self-reload guard: reloading the calling extension would kill the
		// process servicing this very RPC. The bridge knows which extension
		// issued the call (b.caps.Name), so refuse it here.
		if p.Name == b.caps.Name {
			rpcErr = &Error{Code: -32000, Message: "reload_extension: an extension cannot reload itself"}
			break
		}
		if err := api.ReloadExtension(p.Name); err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = okTrue
		}

	case "reload_mcp":
		// No params required. The agent just wants to re-read MCP config
		// from disk and apply changes.
		mcpResult, err := api.ReloadMCP()
		if err != nil {
			rpcErr = &Error{Code: -32000, Message: err.Error()}
		} else {
			result = mcpResult
		}

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
		var p reportProgressParams
		if n.Params != nil {
			_ = json.Unmarshal(*n.Params, &p)
		}
		b.reportProgress(p.Message, api)
	case "provider.stream.event":
		b.handleProviderStreamEvent(n.Params)
	}
}

// jsonRPCIDAsInt converts a JSON-RPC id (decoded as any) into an int when
// possible. Returns 0 for non-numeric or absent ids; the request_id field
// in side_query/delta notifications then echoes 0, which still uniquely
// identifies "the call that has no id" for a given SDK session.
func jsonRPCIDAsInt(id any) int {
	switch v := id.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
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

// failAllPending delivers an error Response to every outstanding CallHook
// waiter and clears the pending map. Called when the bridge's read loop exits
// (extension crash / EOF, or context cancellation) so waiters fail fast
// instead of blocking. This is what bounds a tool with a disabled host-side
// timeout (Timeout < 0) when the extension process dies mid-call: without it
// such a call would hang until the turn context is cancelled. Each pending
// channel is buffered (cap 1) and only ever delivered to once (routeResponse
// and CallHook both delete under pendingMu before use), so the send never
// blocks.
//
// It also latches the bridge closed (with closeErr) under pendingMu, so a
// CallHook racing in *after* the read loop has exited fails fast at
// registration instead of parking a channel in the fresh map that nothing
// will ever complete.
func (b *Bridge) failAllPending(err error) {
	if err == nil {
		err = fmt.Errorf("extension: connection closed")
	}
	b.pendingMu.Lock()
	pend := b.pending
	b.pending = make(map[int64]chan *Response)
	b.closed = true
	b.closeErr = err
	b.pendingMu.Unlock()
	for _, ch := range pend {
		ch <- &Response{Error: &Error{Code: -32000, Message: err.Error()}}
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

// DefaultToolCallTimeout is the host-side timeout applied to an extension's
// tool_call hook when the tool declares no explicit Timeout (ToolSpec.Timeout
// == 0). The wait is activity-aware (see Bridge.CallHook): a chatty extension
// resets the deadline on any inbound traffic, so this bounds only a call that
// goes silent. Overridable via the FIR_EXT_TOOL_TIMEOUT environment variable
// (in seconds); an invalid or non-positive value is ignored.
const DefaultToolCallTimeout = 30 * time.Second

// resolveToolCallTimeout converts a ToolSpec.Timeout (seconds) into the
// duration passed to CallHook. See ToolSpec.Timeout for the semantics:
//
//	declared == 0 -> DefaultToolCallTimeout (or FIR_EXT_TOOL_TIMEOUT override)
//	declared  > 0 -> that many seconds
//	declared  < 0 -> 0 (host-side deadline disabled; call bounded only by ctx)
func resolveToolCallTimeout(declared float64) time.Duration {
	switch {
	case declared < 0:
		return 0 // CallHook treats a non-positive timeout as disabled.
	case declared > 0:
		return time.Duration(declared * float64(time.Second))
	default:
		d := DefaultToolCallTimeout
		if v := os.Getenv("FIR_EXT_TOOL_TIMEOUT"); v != "" {
			if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
				d = time.Duration(secs * float64(time.Second))
			}
		}
		return d
	}
}

// CallHook sends a JSON-RPC request and waits for a response with timeout.
//
// A positive timeout is activity-aware: the deadline resets whenever the
// extension sends us any message (request or response), so an extension that
// stays busy — e.g. aside making repeated call_tool requests — is never
// clipped mid-work. A non-positive timeout disables the host-side deadline
// entirely; the call is then bounded only by ctx (turn cancel / ESC).
func (b *Bridge) CallHook(ctx context.Context, name string, data any, timeout time.Duration) (json.RawMessage, error) {
	codec := b.proc.GetCodec()
	if codec == nil {
		return nil, fmt.Errorf("extension: not connected")
	}

	id := b.nextID.Add(1)
	ch := make(chan *Response, 1)

	b.pendingMu.Lock()
	if b.closed {
		err := b.closeErr
		b.pendingMu.Unlock()
		if err == nil {
			err = fmt.Errorf("extension: connection closed")
		}
		return nil, err
	}
	b.pending[id] = ch
	b.pendingMu.Unlock()

	if err := codec.WriteRequest(int(id), name, data); err != nil {
		b.pendingMu.Lock()
		delete(b.pending, id)
		b.pendingMu.Unlock()
		return nil, err
	}

	// timeout <= 0 disables the host-side deadline: wait only on the
	// response channel or ctx cancellation.
	if timeout <= 0 {
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
		}
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
		// Resolve the declared per-tool timeout once at registration.
		toolTimeout := resolveToolCallTimeout(tool.Timeout)
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

				// Stamp tool_call_id so observable cards written from
				// inside this Execute trace back to this transcript
				// entry (see docs/design/observable-cards.md "Provenance").
				toolCallID := ctx.ToolCallID
				b.activeToolCallID.Store(&toolCallID)
				defer b.activeToolCallID.Store(nil)

				params := ToolCallHookPayload{
					ToolCallID: ctx.ToolCallID,
					Name:       tool.Name,
					Params:     ctx.Params,
				}
				raw, err := b.CallHook(ctx.Context, "tool_call", params, toolTimeout)
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
