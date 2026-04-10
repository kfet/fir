package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/external/poe/internal/access"
	"github.com/kfet/fir/external/poe/internal/mcpnotify"
	"github.com/kfet/fir/external/poe/internal/poe"
	"github.com/kfet/fir/external/poe/internal/router"
)

// listenLocal returns a TCP listener bound to 127.0.0.1 on an
// OS-assigned free port. Used by shutdown tests that need a real
// *http.Server to Shutdown.
func listenLocal(t *testing.T) (net.Listener, error) {
	t.Helper()
	return net.Listen("tcp", "127.0.0.1:0")
}

// --- HTTP surface tests -------------------------------------------------

func TestRootHandler_OK(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	rootHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type: got %q, want text/plain…", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "poe-bridge") {
		t.Errorf("body: missing 'poe-bridge': %q", body)
	}
	if !strings.Contains(body, version) {
		t.Errorf("body: missing version %q: %q", version, body)
	}
	if !strings.HasSuffix(body, "ok\n") {
		t.Errorf("body: want trailing 'ok\\n', got %q", body)
	}
}

func TestRootHandler_MethodsAllAccepted(t *testing.T) {
	// M1 placeholder accepts any method; the real /poe handler in M2 will
	// restrict to POST. This test locks in current behaviour so the M2
	// change is explicit.
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(m, "/", nil)
			rootHandler(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("%s: status %d, want 200", m, rr.Code)
			}
		})
	}
}

func TestNewHTTPHandler_RoutesRoot(t *testing.T) {
	h := newHTTPHandler(http.NotFoundHandler())
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "poe-bridge") {
		t.Errorf("body missing marker: %q", body)
	}
}

func TestNewHTTPHandler_UnknownPathStillMatchesRoot(t *testing.T) {
	// Current mux uses "/" as a catch-all. Locking this in so the M2
	// handler change (which will introduce /poe specifically) is explicit.
	h := newHTTPHandler(http.NotFoundHandler())
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// --- pingResult -----------------------------------------------------------

func TestPingResult_Shape(t *testing.T) {
	r := pingResult()
	if r == nil {
		t.Fatal("pingResult() returned nil")
	}
	if len(r.Content) != 1 {
		t.Fatalf("content len: got %d, want 1", len(r.Content))
	}
	tc, ok := r.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type: got %T, want *mcp.TextContent", r.Content[0])
	}
	if !strings.Contains(tc.Text, "pong") {
		t.Errorf("text missing 'pong': %q", tc.Text)
	}
	if !strings.Contains(tc.Text, version) {
		t.Errorf("text missing version %q: %q", version, tc.Text)
	}
}

// --- newHTTPServer --------------------------------------------------------

func TestNewHTTPServer_Defaults(t *testing.T) {
	h := newHTTPHandler(http.NotFoundHandler())
	s := newHTTPServer(":0", h)
	if s.Addr != ":0" {
		t.Errorf("Addr: got %q, want :0", s.Addr)
	}
	if s.Handler == nil {
		t.Error("Handler: nil")
	}
	if s.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout: got %v, want >0", s.ReadHeaderTimeout)
	}
}

// --- installShutdown ------------------------------------------------------

func TestInstallShutdown_OnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	httpSrv := newHTTPServer(":0", newHTTPHandler(http.NotFoundHandler()))
	ln, err := listenLocal(t)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv.Addr = ln.Addr().String()
	go func() { _ = httpSrv.Serve(ln) }()

	sigCh := make(chan os.Signal, 1)
	done := installShutdown(ctx, cancel, httpSrv, sigCh)

	sigCh <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown goroutine did not exit within 3s")
	}
	if ctx.Err() == nil {
		t.Error("ctx should be cancelled after shutdown")
	}
}

func TestInstallShutdown_OnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httpSrv := newHTTPServer(":0", newHTTPHandler(http.NotFoundHandler()))
	ln, err := listenLocal(t)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv.Addr = ln.Addr().String()
	go func() { _ = httpSrv.Serve(ln) }()

	sigCh := make(chan os.Signal, 1)
	done := installShutdown(ctx, cancel, httpSrv, sigCh)

	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown goroutine did not exit within 3s")
	}
}

// connectInMemory wires up a bridge MCP server (using the provided
// router) to a client over an in-memory pipe transport and returns the
// live ClientSession.
func connectInMemory(t *testing.T, rt *router.Router) (*mcp.ClientSession, func()) {
	t.Helper()

	if rt == nil {
		rt = router.New()
	}
	srv := newMCPServer(rt)
	serverT, clientT := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())

	// Run the server in a goroutine; it exits when ctx is cancelled or the
	// client closes its end of the pipe.
	srvDone := make(chan error, 1)
	go func() {
		srvDone <- srv.Run(ctx, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		_ = session.Close()
		cancel()
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
			t.Error("server did not exit within 2s")
		}
	}
	return session, cleanup
}

func TestMCPServer_ListTools(t *testing.T) {
	session, cleanup := connectInMemory(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("tools len: got %d, want 2 (ping + reply)", len(res.Tools))
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
		if tl.Description == "" {
			t.Errorf("tool %q has empty description", tl.Name)
		}
	}
	for _, want := range []string{"ping", "reply"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestMCPServer_CallPing(t *testing.T) {
	session, cleanup := connectInMemory(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool returned IsError: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content len: got %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type: got %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "pong") {
		t.Errorf("text missing 'pong': %q", tc.Text)
	}
	if !strings.Contains(tc.Text, version) {
		t.Errorf("text missing version %q: %q", version, tc.Text)
	}
}

// --- reply tool --------------------------------------------------------

func TestMCPServer_CallReply_Ok(t *testing.T) {
	rt := router.New()
	session, cleanup := connectInMemory(t, rt)
	defer cleanup()

	// A receiver must exist on the router before Push can succeed.
	ch := rt.Register("m-reply")
	defer rt.Unregister("m-reply")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"message_id": "m-reply",
			"text":       "hello",
			"final":      false,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("reply tool returned IsError: %+v", res)
	}

	select {
	case c := <-ch:
		if c.Text != "hello" || c.Final {
			t.Errorf("chunk: got %+v", c)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("no chunk arrived after reply tool call")
	}
}

func TestMCPServer_CallReply_UnknownMessage(t *testing.T) {
	rt := router.New()
	session, cleanup := connectInMemory(t, rt)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"message_id": "m-does-not-exist",
			"text":       "hi",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for unknown message_id")
	}
}

func TestMCPServer_CallReply_MissingMessageID(t *testing.T) {
	session, cleanup := connectInMemory(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"text": "no id",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for missing message_id")
	}
}

// --- OnQuery end-to-end --------------------------------------------------

// stubNotifier counts SendChannel calls and records payloads; it implements
// the channelSender interface used by newOnQuery.
type stubNotifier struct {
	mu    sync.Mutex
	calls []mcpnotify.ChannelMessage
	fail  error
}

func (s *stubNotifier) SendChannel(_ context.Context, msg mcpnotify.ChannelMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.calls = append(s.calls, msg)
	return nil
}

// TestOnQuery_EndToEnd drives the full happy path through the real
// newOnQuery closure: POST a query, push two chunks via the router (as if
// the reply MCP tool had been called), then a final chunk, and assert the
// SSE stream contains meta → two texts → done in order.
func TestOnQuery_EndToEnd(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}

	h := &poe.Handler{
		AccessKey: "k",
		BotName:   "e2e",
		OnQuery:   newOnQuery(rt, notif, "e2e", nil, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// In a goroutine: push chunks via the router once the SSE handler has
	// registered. We busy-poll the router len to detect registration.
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if rt.Len() > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if rt.Len() == 0 {
			t.Errorf("router never saw a registration")
			return
		}
		if err := rt.Push("m-e2e", router.Chunk{Text: "part-1 "}); err != nil {
			t.Errorf("push 1: %v", err)
			return
		}
		if err := rt.Push("m-e2e", router.Chunk{Text: "part-2"}); err != nil {
			t.Errorf("push 2: %v", err)
			return
		}
		if err := rt.Push("m-e2e", router.Chunk{Final: true}); err != nil {
			t.Errorf("push final: %v", err)
			return
		}
	}()

	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"hello","content_type":"text/markdown","timestamp":1,"message_id":"m-in"}],"user_id":"u-a","conversation_id":"c-b","message_id":"m-e2e"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	<-pushDone

	s := string(out)

	metaIdx := strings.Index(s, "event: meta")
	text1Idx := strings.Index(s, "part-1")
	text2Idx := strings.Index(s, "part-2")
	doneIdx := strings.Index(s, "event: done")
	if metaIdx < 0 || text1Idx < 0 || text2Idx < 0 || doneIdx < 0 {
		t.Fatalf("missing markers in output:\n%s", s)
	}
	if !(metaIdx < text1Idx && text1Idx < text2Idx && text2Idx < doneIdx) {
		t.Errorf("ordering wrong: meta=%d p1=%d p2=%d done=%d", metaIdx, text1Idx, text2Idx, doneIdx)
	}

	// Notifier was called exactly once with the inbound text + ids.
	notif.mu.Lock()
	calls := notif.calls
	notif.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("notif calls: got %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].Content, "hello") {
		t.Errorf("notif content missing hello: %q", calls[0].Content)
	}
	if calls[0].Meta["message_id"] != "m-e2e" {
		t.Errorf("notif meta.message_id: got %v", calls[0].Meta["message_id"])
	}
}

// TestNewOnQuery_MissingMessageID locks in that the production newOnQuery
// closure rejects a query with no message_id up front (before touching
// the router or notifier).
func TestNewOnQuery_MissingMessageID(t *testing.T) {
	rt := router.New()
	// A notifier whose connection is never captured will surface
	// ErrNotConnected, but we should never reach it here.
	notif := mcpnotify.NewNotifier()
	fn := newOnQuery(rt, notif, "bot", nil, nil)

	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	err = fn(context.Background(), &poe.QueryRequest{}, sse)
	if err == nil || !strings.Contains(err.Error(), "message_id") {
		t.Errorf("err: got %v, want message_id error", err)
	}
	if rt.Len() != 0 {
		t.Errorf("router should be empty, got %d", rt.Len())
	}
}

// --- Full pipeline e2e: MCP client → reply tool → router → SSE ----------

// TestFullPipeline_MCPReplyToSSE wires up the MCP server with a shared
// router, starts an HTTP server with the real newOnQuery hook, POSTs a
// query, and drives the reply tool from a real MCP client session.
// Asserts that SSE text events appear from the tool calls and the done
// event is emitted after final=true.
func TestFullPipeline_MCPReplyToSSE(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}

	// MCP server with our shared router.
	session, cleanupMCP := connectInMemory(t, rt)
	defer cleanupMCP()

	// HTTP server with the real newOnQuery.
	h := &poe.Handler{
		AccessKey: "k",
		BotName:   "pipeline",
		OnQuery:   newOnQuery(rt, notif, "pipeline", nil, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// POST the query in a goroutine; it blocks until the SSE stream ends.
	type result struct {
		body string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"test msg","content_type":"text/markdown","timestamp":1,"message_id":"m-in"}],"user_id":"u-pipe","conversation_id":"c-pipe","message_id":"m-pipe"}`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer k")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		resultCh <- result{body: string(out), err: err}
	}()

	// Wait for the OnQuery hook to register the message_id on the router.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && rt.Len() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.Len() == 0 {
		t.Fatal("router never saw a registration from OnQuery")
	}

	// Drive the reply tool from the MCP client — just like fir would.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First chunk.
	res1, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"message_id": "m-pipe",
			"text":       "reply-part-1 ",
		},
	})
	if err != nil {
		t.Fatalf("reply 1: %v", err)
	}
	if res1.IsError {
		t.Fatalf("reply 1 IsError: %+v", res1)
	}

	// Second chunk.
	res2, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"message_id": "m-pipe",
			"text":       "reply-part-2",
		},
	})
	if err != nil {
		t.Fatalf("reply 2: %v", err)
	}
	if res2.IsError {
		t.Fatalf("reply 2 IsError: %+v", res2)
	}

	// Final chunk.
	res3, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reply",
		Arguments: map[string]any{
			"message_id": "m-pipe",
			"text":       "",
			"final":      true,
		},
	})
	if err != nil {
		t.Fatalf("reply final: %v", err)
	}
	if res3.IsError {
		t.Fatalf("reply final IsError: %+v", res3)
	}

	// Wait for the HTTP response.
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("http result: %v", r.err)
		}
		s := r.body
		metaIdx := strings.Index(s, "event: meta")
		p1Idx := strings.Index(s, "reply-part-1")
		p2Idx := strings.Index(s, "reply-part-2")
		doneIdx := strings.Index(s, "event: done")
		if metaIdx < 0 || p1Idx < 0 || p2Idx < 0 || doneIdx < 0 {
			t.Fatalf("missing markers in SSE:\n%s", s)
		}
		if !(metaIdx < p1Idx && p1Idx < p2Idx && p2Idx < doneIdx) {
			t.Errorf("ordering wrong: meta=%d p1=%d p2=%d done=%d", metaIdx, p1Idx, p2Idx, doneIdx)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP response timed out")
	}

	// Verify notifier received the channel message.
	notif.mu.Lock()
	calls := notif.calls
	notif.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("notifier calls: got %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].Content, "test msg") {
		t.Errorf("notifier content missing 'test msg': %q", calls[0].Content)
	}
	if calls[0].Meta["message_id"] != "m-pipe" {
		t.Errorf("notifier meta.message_id: got %v", calls[0].Meta["message_id"])
	}
	if calls[0].Meta["source"] != "poe" {
		t.Errorf("notifier meta.source: got %v", calls[0].Meta["source"])
	}
}

// --- OnQuery: notifier failure path --------------------------------------

func TestOnQuery_NotifierFails(t *testing.T) {
	rt := router.New()
	failNotif := &stubNotifier{fail: errors.New("notifier down")}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newOnQuery(rt, failNotif, "bot", nil, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"x","message_id":"m-x"}],"user_id":"u-x","conversation_id":"c-x","message_id":"m-nfail"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	// Should contain meta (always first), then error event, then done.
	if !strings.Contains(s, "event: meta") {
		t.Error("missing meta")
	}
	if !strings.Contains(s, "event: error") {
		t.Errorf("missing error event in:\n%s", s)
	}
	if !strings.Contains(s, "notifier down") {
		t.Errorf("error text not in output: %s", s)
	}
	if !strings.Contains(s, "event: done") {
		t.Error("missing done after error")
	}
	// Router should have been cleaned up.
	if rt.Len() != 0 {
		t.Errorf("router leak: %d entries", rt.Len())
	}
}

// --- OnQuery: context cancellation path ----------------------------------

func TestOnQuery_ContextCancelled(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}

	// Use a very short timeout to trigger the context cancellation path
	// inside newOnQuery without waiting for a reply.
	shortOnQuery := func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
		ch := rt.Register(q.MessageID)
		defer rt.Unregister(q.MessageID)
		if err := notif.SendChannel(ctx, mcpnotify.ChannelMessage{Content: "x"}); err != nil {
			return err
		}
		// Use a tiny context timeout so the select hits ctx.Done.
		shortCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		select {
		case <-shortCtx.Done():
			return shortCtx.Err()
		case c, ok := <-ch:
			if !ok {
				return nil
			}
			if c.Text != "" {
				_ = sse.WriteEvent("text", map[string]any{"text": c.Text})
			}
			if c.Final {
				_ = sse.WriteEvent("done", map[string]any{})
			}
			return nil
		}
	}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   shortOnQuery,
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"version":"1.0","type":"query","query":[],"user_id":"u-t","conversation_id":"c-t","message_id":"m-timeout"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	// The handler should have emitted meta, then an error event (from
	// handleQuery's OnQuery-error path) with the context deadline text,
	// then done.
	if !strings.Contains(s, "event: meta") {
		t.Error("missing meta")
	}
	if !strings.Contains(s, "event: error") {
		t.Errorf("missing error event in:\n%s", s)
	}
	if !strings.Contains(s, "event: done") {
		t.Error("missing done")
	}
	if rt.Len() != 0 {
		t.Errorf("router leak: %d entries", rt.Len())
	}
}

// --- Pairing flow tests --------------------------------------------------

func TestOnQuery_UnpairedUser_GetsPairingCode(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}
	acl, err := access.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newOnQuery(rt, notif, "bot", acl, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"hello","message_id":"m-a"}],"user_id":"u-stranger","conversation_id":"c-a","message_id":"m-pair1"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	// Must contain pairing instructions, NOT the user's message forwarded.
	if !strings.Contains(s, "Not paired") {
		t.Errorf("missing pairing message in:\n%s", s)
	}
	if !strings.Contains(s, "/poe:access pair") {
		t.Errorf("missing pair command in:\n%s", s)
	}
	if !strings.Contains(s, "event: done") {
		t.Error("missing done event")
	}
	// Notifier should NOT have been called (message not forwarded to fir).
	notif.mu.Lock()
	nc := len(notif.calls)
	notif.mu.Unlock()
	if nc != 0 {
		t.Errorf("notifier calls: got %d, want 0 (unpaired user)", nc)
	}
	// Router should be clean.
	if rt.Len() != 0 {
		t.Errorf("router leak: %d", rt.Len())
	}
}

func TestOnQuery_PairedUser_GetsThrough(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}
	dir := t.TempDir()
	acl, _ := access.NewStore(dir)

	// Pair the user: generate code then consume it.
	code, _ := acl.GenerateCode("u-friend")
	_, err := acl.Pair(code)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newOnQuery(rt, notif, "bot", acl, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Push a final chunk once registered so the handler doesn't block forever.
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && rt.Len() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		_ = rt.Push("m-paired", router.Chunk{Text: "got it", Final: true})
	}()

	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"real msg","message_id":"m-in"}],"user_id":"u-friend","conversation_id":"c-b","message_id":"m-paired"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	// Should have forwarded to fir (notifier called) and streamed the reply.
	if strings.Contains(s, "Not paired") {
		t.Errorf("paired user got pairing prompt:\n%s", s)
	}
	if !strings.Contains(s, "got it") {
		t.Errorf("missing reply text in:\n%s", s)
	}
	notif.mu.Lock()
	nc := len(notif.calls)
	notif.mu.Unlock()
	if nc != 1 {
		t.Errorf("notifier calls: got %d, want 1", nc)
	}
}

func TestOnQuery_SameUnpairedUser_GetsSameCode(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}
	acl, _ := access.NewStore(t.TempDir())

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newOnQuery(rt, notif, "bot", acl, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// First request: get a code.
	body := `{"version":"1.0","type":"query","query":[],"user_id":"u-repeat","conversation_id":"c-r","message_id":"m-r1"}`
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req1.Header.Set("Authorization", "Bearer k")
	resp1, _ := http.DefaultClient.Do(req1)
	out1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	// Second request: should get the same code.
	body2 := `{"version":"1.0","type":"query","query":[],"user_id":"u-repeat","conversation_id":"c-r","message_id":"m-r2"}`
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer k")
	resp2, _ := http.DefaultClient.Do(req2)
	out2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	// Extract the 6-char hex code from both responses; they should match.
	extractCode := func(s string) string {
		idx := strings.Index(s, "pair ")
		if idx < 0 || idx+11 > len(s) {
			return ""
		}
		return s[idx+5 : idx+11]
	}
	c1 := extractCode(string(out1))
	c2 := extractCode(string(out2))
	if c1 == "" || c2 == "" {
		t.Fatalf("couldn't extract codes from responses")
	}
	if c1 != c2 {
		t.Errorf("codes differ: %q vs %q", c1, c2)
	}
}

// --- M7: Multi-conversation verification --------------------------------

func TestMultiConversation_SeparateThreads(t *testing.T) {
	rt := router.New()
	notif := &stubNotifier{}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newOnQuery(rt, notif, "multi", nil, nil),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Launch two concurrent queries with different conversation_ids.
	type result struct {
		convID string
		body   string
		err    error
	}
	resultCh := make(chan result, 2)

	for _, conv := range []struct{ convID, msgID, text string }{
		{"c-alpha", "m-alpha", "msg from alpha"},
		{"c-beta", "m-beta", "msg from beta"},
	} {
		conv := conv
		go func() {
			body := fmt.Sprintf(`{"version":"1.0","type":"query","query":[{"role":"user","content":"%s","message_id":"m-in"}],"user_id":"u-multi","conversation_id":"%s","message_id":"%s"}`,
				conv.text, conv.convID, conv.msgID)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer k")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				resultCh <- result{convID: conv.convID, err: err}
				return
			}
			defer resp.Body.Close()
			out, _ := io.ReadAll(resp.Body)
			resultCh <- result{convID: conv.convID, body: string(out)}
		}()
	}

	// Wait for both to register on the router.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && rt.Len() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.Len() < 2 {
		t.Fatalf("only %d registrations, want 2", rt.Len())
	}

	// Push distinct replies to each conversation.
	_ = rt.Push("m-alpha", router.Chunk{Text: "reply-alpha", Final: true})
	_ = rt.Push("m-beta", router.Chunk{Text: "reply-beta", Final: true})

	// Collect results.
	results := map[string]string{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-resultCh:
			if r.err != nil {
				t.Fatalf("conv %s: %v", r.convID, r.err)
			}
			results[r.convID] = r.body
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for responses")
		}
	}

	// Alpha got alpha's reply, beta got beta's.
	if !strings.Contains(results["c-alpha"], "reply-alpha") {
		t.Errorf("alpha got wrong reply: %s", results["c-alpha"])
	}
	if strings.Contains(results["c-alpha"], "reply-beta") {
		t.Errorf("alpha leaked beta's reply")
	}
	if !strings.Contains(results["c-beta"], "reply-beta") {
		t.Errorf("beta got wrong reply: %s", results["c-beta"])
	}
	if strings.Contains(results["c-beta"], "reply-alpha") {
		t.Errorf("beta leaked alpha's reply")
	}

	// Notifier should have received two separate calls with distinct conv ids.
	notif.mu.Lock()
	calls := notif.calls
	notif.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("notifier: got %d calls want 2", len(calls))
	}
	convIDs := map[string]bool{}
	for _, c := range calls {
		convIDs[c.Meta["conversation_id"].(string)] = true
	}
	if !convIDs["c-alpha"] || !convIDs["c-beta"] {
		t.Errorf("notifier convIDs: %v", convIDs)
	}
}
