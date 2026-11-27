package mcp

// extConnection lets fir speak MCP extension methods the go-sdk does not
// know (skills/list, skills/get, tasks/get, tasks/update, tasks/cancel).
//
// It sits between the SDK and the real connection. A pump goroutine owns the
// inner Read; responses to fir's own requests (string ids with extIDPrefix)
// go to their waiting Call and never reach the SDK. Everything else is handed
// to the SDK's Read in order. Because the SDK only sees messages through
// Read, the connection can also hold back a response (a task handle) and
// deliver a different one later under the same id — see tasks.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// extIDPrefix marks request ids fir issues itself. The SDK uses numeric ids,
// so a string id with this prefix never collides with one of its calls.
const extIDPrefix = "fir-ext-"

// errExtClosed is returned by Call after the connection closed.
var errExtClosed = errors.New("mcp: connection closed")

// extTransport wraps an sdk.Transport so every connection it makes is an
// extConnection. The latest connection is kept for Manager-side calls.
type extTransport struct {
	inner sdk.Transport
	// tasks, when non-nil, turns task-handle results into final results.
	tasks *taskRunner
	// hideElicit removes the elicitation capability from what fir sends:
	// no front end can answer, so the SDK's handler only ever declines.
	hideElicit bool
	conn       atomic.Pointer[extConnection]
	// logLevel, when set, is added as per-request _meta on stateless
	// (2026-07-28+) requests, which replaces logging/setLevel.
	logLevel atomic.Pointer[sdk.LoggingLevel]
}

func (t *extTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	inner, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	c := newExtConnection(inner, t.tasks)
	c.hideElicit = t.hideElicit
	c.logLevel = &t.logLevel
	t.conn.Store(c)
	return c, nil
}

// current returns the latest connection, or nil before Connect.
func (t *extTransport) current() *extConnection { return t.conn.Load() }

type readResult struct {
	msg jsonrpc.Message
	err error
}

type extConnection struct {
	sdk.Connection
	tasks      *taskRunner
	hideElicit bool
	logLevel   *atomic.Pointer[sdk.LoggingLevel]

	in     chan readResult // messages for the SDK, in order
	inject chan jsonrpc.Message
	done   chan struct{}
	close  sync.Once

	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[string]chan *jsonrpc.Response
	// sdkCalls maps an SDK request id (as a JSON string) to its method, for
	// the calls whose results may be task handles.
	sdkCalls map[string]string
	// held maps an SDK request id to the cancel func of its task poller.
	held map[string]context.CancelFunc
	wg   sync.WaitGroup
}

func newExtConnection(inner sdk.Connection, tasks *taskRunner) *extConnection {
	c := &extConnection{
		Connection: inner,
		tasks:      tasks,
		in:         make(chan readResult),
		inject:     make(chan jsonrpc.Message),
		done:       make(chan struct{}),
		pending:    map[string]chan *jsonrpc.Response{},
		sdkCalls:   map[string]string{},
		held:       map[string]context.CancelFunc{},
	}
	c.wg.Add(1)
	go c.pump()
	return c
}

// pump reads the inner connection until it fails or the connection closes.
func (c *extConnection) pump() {
	defer c.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		msg, err := c.Connection.Read(ctx)
		if err == nil {
			if resp, ok := msg.(*jsonrpc.Response); ok && c.routeResponse(resp) {
				continue
			}
		}
		select {
		case c.in <- readResult{msg, err}:
		case <-c.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// routeResponse delivers resp to fir's own waiter, or starts a task poller
// for it. It reports whether resp was consumed.
func (c *extConnection) routeResponse(resp *jsonrpc.Response) bool {
	key := idKey(resp.ID)
	c.mu.Lock()
	if ch, ok := c.pending[key]; ok {
		delete(c.pending, key)
		c.mu.Unlock()
		ch <- resp
		return true
	}
	method, tracked := c.sdkCalls[key]
	delete(c.sdkCalls, key)
	c.mu.Unlock()
	if !tracked || c.tasks == nil || resp.Error != nil {
		return false
	}
	task, ok := parseTaskHandle(resp.Result)
	if !ok {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.held[key] = cancel
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel()
		final := c.tasks.run(ctx, c, method, resp.ID, task)
		c.mu.Lock()
		delete(c.held, key)
		c.mu.Unlock()
		if final == nil {
			return
		}
		select {
		case c.inject <- final:
		case <-c.done:
		}
	}()
	return true
}

func (c *extConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case r := <-c.in:
		return r.msg, r.err
	case m := <-c.inject:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errExtClosed
	}
}

func (c *extConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if req, ok := msg.(*jsonrpc.Request); ok {
		if c.hideElicit && len(req.Params) > 0 {
			if p, changed := stripElicitationCap(req.Params); changed {
				req.Params = p
			}
		}
		if c.logLevel != nil && req.ID.IsValid() {
			if lvl := c.logLevel.Load(); lvl != nil {
				if p, changed := addLogLevelMeta(req.Params, *lvl); changed {
					req.Params = p
				}
			}
		}
		switch {
		case req.ID.IsValid() && c.tasks != nil && taskCapableMethod(req.Method):
			c.mu.Lock()
			c.sdkCalls[idKey(req.ID)] = req.Method
			c.mu.Unlock()
		case req.Method == "notifications/cancelled":
			// The SDK gave up on a call. If fir holds it as a task, stop
			// polling; the poller cancels the task on the server.
			var p struct {
				RequestID any `json:"requestId"`
			}
			if json.Unmarshal(req.Params, &p) == nil {
				if id, err := jsonrpc.MakeID(p.RequestID); err == nil {
					c.mu.Lock()
					cancel := c.held[idKey(id)]
					delete(c.sdkCalls, idKey(id))
					c.mu.Unlock()
					if cancel != nil {
						cancel()
					}
				}
			}
		}
	}
	return c.Connection.Write(ctx, msg)
}

func (c *extConnection) Close() error {
	var err error
	c.close.Do(func() {
		close(c.done)
		err = c.Connection.Close()
		c.mu.Lock()
		for _, cancel := range c.held {
			cancel()
		}
		c.mu.Unlock()
	})
	c.wg.Wait()
	return err
}

// Call sends method with params to the server and decodes the result into
// out (which may be nil).
func (c *extConnection) Call(ctx context.Context, method string, params, out any) error {
	id, _ := jsonrpc.MakeID(fmt.Sprintf("%s%d", extIDPrefix, c.nextID.Add(1)))
	if params == nil {
		params = struct{}{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := &jsonrpc.Request{ID: id, Method: method, Params: raw}
	ch := make(chan *jsonrpc.Response, 1)
	key := idKey(id)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()
	if err := c.Connection.Write(ctx, req); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errExtClosed
	}
}

// idKey renders an id as a map key that keeps string and number ids apart.
func idKey(id jsonrpc.ID) string {
	switch v := id.Raw().(type) {
	case string:
		return "s:" + v
	case int64:
		return fmt.Sprintf("n:%d", v)
	case float64:
		return fmt.Sprintf("n:%d", int64(v))
	default:
		return fmt.Sprintf("?:%v", v)
	}
}

// clientCapsMetaKey is where 2026-07-28 requests carry client capabilities.
const clientCapsMetaKey = "io.modelcontextprotocol/clientCapabilities"

// stripElicitationCap removes "elicitation" from the client capabilities in
// request params: params.capabilities (initialize) and the per-request
// clientCapabilities _meta. It reports whether params changed.
func stripElicitationCap(params json.RawMessage) (json.RawMessage, bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil {
		return params, false
	}
	changed := false
	if caps, ok := dropKey(p["capabilities"], "elicitation"); ok {
		p["capabilities"], changed = caps, true
	}
	if raw, ok := p["_meta"]; ok {
		var meta map[string]json.RawMessage
		if json.Unmarshal(raw, &meta) == nil {
			if caps, ok := dropKey(meta[clientCapsMetaKey], "elicitation"); ok {
				meta[clientCapsMetaKey] = caps
				if b, err := json.Marshal(meta); err == nil {
					p["_meta"], changed = b, true
				}
			}
		}
	}
	if !changed {
		return params, false
	}
	out, err := json.Marshal(p)
	if err != nil {
		return params, false
	}
	return out, true
}

// dropKey deletes key from the JSON object raw. ok is false when raw is not
// an object holding key.
func dropKey(raw json.RawMessage, key string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw, false
	}
	if _, ok := obj[key]; !ok {
		return raw, false
	}
	delete(obj, key)
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, false
	}
	return out, true
}

// addLogLevelMeta adds the per-request log level to params of a stateless
// request: one whose _meta carries a protocol version. Requests without one
// belong to a legacy session, where logging/setLevel sets the level instead.
func addLogLevelMeta(params json.RawMessage, lvl sdk.LoggingLevel) (json.RawMessage, bool) {
	var p map[string]json.RawMessage
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return params, false
	}
	var meta map[string]json.RawMessage
	if raw, ok := p["_meta"]; !ok || json.Unmarshal(raw, &meta) != nil {
		return params, false
	}
	if _, ok := meta[sdk.MetaKeyProtocolVersion]; !ok {
		return params, false
	}
	if _, ok := meta[sdk.MetaKeyLogLevel]; ok {
		return params, false
	}
	meta[sdk.MetaKeyLogLevel], _ = json.Marshal(lvl)
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return params, false
	}
	p["_meta"] = rawMeta
	out, err := json.Marshal(p)
	if err != nil {
		return params, false
	}
	return out, true
}
