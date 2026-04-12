// Command poe-bridge is a Poe server-bot ↔ fir MCP channel bridge.
//
// It runs two I/O surfaces in one process: an MCP stdio server (talking
// to fir) and an HTTPS endpoint (talking to Poe). Inbound Poe queries
// are forwarded to fir as notifications/claude/channel messages; fir
// replies by calling the `reply` MCP tool, whose handler pushes chunks
// through a router into the SSE response stream back to Poe.
// See external/poe/README.md for the full design.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/external/poe/internal/access"
	agentPkg "github.com/kfet/fir/external/poe/internal/agent"
	"github.com/kfet/fir/external/poe/internal/funnel"
	"github.com/kfet/fir/external/poe/internal/mcpnotify"
	"github.com/kfet/fir/external/poe/internal/permq"
	"github.com/kfet/fir/external/poe/internal/poe"
	relayPkg "github.com/kfet/fir/external/poe/internal/relay"
	"github.com/kfet/fir/external/poe/internal/router"
	"github.com/kfet/fir/external/poe/internal/selfupdate"
)

const (
	version  = "0.2.0"
	httpAddr = ":8080"

	// queryTimeout bounds how long a single Poe query may occupy the SSE
	// stream while waiting for fir chunks. The Poe protocol itself caps
	// responses at 3600s; we stay well under that to leave slack for the
	// final `done` event to flush.
	queryTimeout = 50 * time.Minute
)

// pingArgs is the empty input schema for the ping tool.
type pingArgs struct{}

// replyArgs is the input schema for the `reply` MCP tool that fir calls
// to stream chunks back to a live Poe query.
type replyArgs struct {
	MessageID string `json:"message_id" jsonschema:"the Poe message_id from the inbound channel notification"`
	Text      string `json:"text" jsonschema:"text chunk to append to the reply; may be empty on a final=true call"`
	Final     bool   `json:"final,omitempty" jsonschema:"set true to mark the last chunk; closes the SSE stream on the Poe side"`
}

// --- HTTP surface ---------------------------------------------------------

func newHTTPHandler(poeHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.Handle("/poe", poeHandler)
	return mux
}

func rootHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "poe-bridge %s ok\n", version)
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// --- OnQuery: the core fir handoff ---------------------------------------

// channelSender is the subset of *mcpnotify.Notifier that newOnQuery
// needs. Extracted as an interface so tests can substitute a stub.
type channelSender interface {
	SendChannel(ctx context.Context, msg mcpnotify.ChannelMessage) error
}

// newOnQuery returns a poe.Handler.OnQuery hook that:
//  1. Registers the incoming message_id with the router.
//  2. Emits a `notifications/claude/channel` notification to fir carrying
//     the user's message text + meta (user_id, conversation_id, message_id).
//  3. Loops reading chunks from the router channel, writing each as an SSE
//     `text` event, until a Final chunk arrives, the request context is
//     cancelled, or the per-query timeout expires.
//  4. Always emits a `done` event on normal completion and always
//     unregisters the message_id.
func newOnQuery(rt *router.Router, notif channelSender, botName string, acl *access.Store, pq *permq.Queue) func(context.Context, *poe.QueryRequest, *poe.SSEWriter) error {
	return func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
		msgID := q.MessageID
		if msgID == "" {
			return fmt.Errorf("poe query missing message_id")
		}

		// --- Access check / pairing gate ---
		if acl != nil && !acl.IsAllowed(q.UserID) {
			code, err := acl.GenerateCode(q.UserID)
			if err != nil {
				return fmt.Errorf("pairing: %w", err)
			}
			pairMsg := fmt.Sprintf(
				"🔑 **Not paired.** In your fir terminal session, run:\n\n```\n/poe:access pair %s\n```\n\nThis code expires in 10 minutes.",
				code,
			)
			if err := sse.WriteEvent("text", map[string]any{"text": pairMsg}); err != nil {
				return err
			}
			return sse.WriteEvent("done", map[string]any{})
		}

		ch := rt.Register(msgID)
		defer rt.Unregister(msgID)
		log.Printf("[onquery] registered msg=%s user=%s conv=%s", msgID, q.UserID, q.ConversationID)

		// Drain any pending permission requests for this user.
		if pq != nil {
			if permText := pq.FormatDrain(q.UserID); permText != "" {
				if err := sse.WriteEvent("text", map[string]any{"text": permText}); err != nil {
					return err
				}
			}
		}

		// Pull the latest user message text out of the query history.
		userText := ""
		if len(q.Query) > 0 {
			userText = q.Query[len(q.Query)-1].Content
		}

		// Include message_id and conversation_id in the content so the model
		// can extract them for the reply tool call. Fir's channel injection
		// only shows Content + source/server in the header; meta fields are
		// not surfaced to the model.
		content := fmt.Sprintf("<poe message_id=\"%s\" conversation_id=\"%s\" user_id=\"%s\">\n%s\n</poe>",
			msgID, q.ConversationID, q.UserID, userText)

		if err := notif.SendChannel(ctx, mcpnotify.ChannelMessage{
			Content: content,
			Meta: map[string]any{
				"source":          "poe",
				"bot":             botName,
				"user":            q.UserID,
				"user_id":         q.UserID,
				"conversation_id": q.ConversationID,
				"message_id":      msgID,
			},
		}); err != nil {
			log.Printf("[onquery] notify fir failed: %v", err)
			return fmt.Errorf("notify fir: %w", err)
		}

		deadline, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()

		for {
			select {
			case <-deadline.Done():
				log.Printf("[onquery] timeout for msg=%s", msgID)
				return deadline.Err()
			case chunk := <-ch:
				log.Printf("[onquery] chunk for msg=%s len=%d final=%v", msgID, len(chunk.Text), chunk.Final)
				// a clean end-of-stream.
				if chunk.Text != "" {
					if err := sse.WriteEvent("text", map[string]any{"text": chunk.Text}); err != nil {
						return err
					}
				}
				if chunk.Final {
					_ = sse.WriteEvent("done", map[string]any{})
					return nil
				}
			}
		}
	}
}

// --- MCP server ----------------------------------------------------------

func newMCPServer(rt *router.Router) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "poe-bridge",
		Version: version,
	}, &mcp.ServerOptions{
		Capabilities: func() *mcp.ServerCapabilities {
			c := &mcp.ServerCapabilities{}
			// Advertise the experimental claude/channel capability so fir
			// knows this server speaks the channel notification protocol.
			c.AddExtension("claude/channel", map[string]any{})
			return c
		}(),
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ping",
		Description: "Smoke-test tool. Returns 'pong' and the bridge version.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ pingArgs) (*mcp.CallToolResult, any, error) {
		return pingResult(), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reply",
		Description: "Append a chunk to the reply for a live Poe query. Set final=true on the last chunk to close the stream.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args replyArgs) (*mcp.CallToolResult, any, error) {
		if args.MessageID == "" {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "reply: message_id is required"}},
			}, nil, nil
		}
		log.Printf("[reply] push msg=%s len=%d final=%v", args.MessageID, len(args.Text), args.Final)
		if err := rt.Push(args.MessageID, router.Chunk{Text: args.Text, Final: args.Final}); err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "reply: " + err.Error()}},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
		}, nil, nil
	})

	return srv
}

func pingResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("pong from poe-bridge %s", version)},
		},
	}
}

// --- shutdown helpers -----------------------------------------------------

func installShutdown(ctx context.Context, cancel context.CancelFunc, httpSrv *http.Server, sigCh <-chan os.Signal) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-sigCh:
			log.Printf("received %s, shutting down", sig)
		case <-ctx.Done():
		}
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}()
	return done
}

// --- main -----------------------------------------------------------------

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Handle --self-update before anything else.
	if len(os.Args) > 1 && os.Args[1] == "--self-update" {
		log.Printf("poe-bridge %s: checking for updates...", version)
		newVer, err := selfupdate.Apply(context.Background(), version)
		if err != nil {
			log.Fatalf("self-update failed: %v", err)
		}
		log.Printf("updated to %s — restart the bridge to use the new version", newVer)
		return
	}

	// Mode dispatch: --relay starts relay mode, --agent starts agent mode,
	// default is v1 single-bridge mode.
	if len(os.Args) > 1 && os.Args[1] == "--relay" {
		runRelay()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--agent" {
		runAgent()
		return
	}

	runV1()
}

// runRelay starts the relay: Poe HTTP frontend + agent websocket listener.
func runRelay() {
	log.Printf("poe-bridge %s relay mode", version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	hub := relayPkg.NewHub()

	// Access control.
	var acl *access.Store
	if stateDir := os.Getenv("POE_STATE_DIR"); stateDir != "" {
		var err error
		acl, err = access.NewStore(stateDir)
		if err != nil {
			log.Fatalf("access store: %v", err)
		}
		log.Printf("access store: %d allowed", len(acl.AllowFrom()))
	}

	agentPort := os.Getenv("POE_AGENT_PORT")
	if agentPort == "" {
		agentPort = "9090"
	}

	// Poe HTTP handler routes queries through the hub.
	poeHandler := &poe.Handler{
		AccessKey: os.Getenv("POE_ACCESS_KEY"),
		BotName:   os.Getenv("POE_BOT_NAME"),
		OnQuery: func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
			// Access check.
			if acl != nil && !acl.IsAllowed(q.UserID) {
				code, err := acl.GenerateCode(q.UserID)
				if err != nil {
					return fmt.Errorf("pairing: %w", err)
				}
				msg := fmt.Sprintf("🔑 **Not paired.** Run in your fir terminal:\n\n```\n/poe:access pair %s\n```", code)
				_ = sse.WriteEvent("text", map[string]any{"text": msg})
				return sse.WriteEvent("done", map[string]any{})
			}

			userText := ""
			if len(q.Query) > 0 {
				userText = q.Query[len(q.Query)-1].Content
			}
			queryJSON, _ := json.Marshal(q.Query)

			replyCh, _ := hub.RouteQuery(q.ConversationID, q.MessageID, q.UserID, userText, queryJSON)
			for c := range replyCh {
				if c.Text != "" {
					if err := sse.WriteEvent("text", map[string]any{"text": c.Text}); err != nil {
						return err
					}
				}
				if c.Final {
					return sse.WriteEvent("done", map[string]any{})
				}
			}
			return sse.WriteEvent("done", map[string]any{})
		},
	}

	// Poe HTTP on :8080.
	poeMux := http.NewServeMux()
	poeMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "poe-bridge %s relay ok\n", version)
	})
	poeMux.Handle("/poe", poeHandler)
	// OAuth callback route — receives redirects from OAuth providers,
	// forwards the result to the agent that registered the session.
	poeMux.HandleFunc("/oauth/cb/", func(w http.ResponseWriter, r *http.Request) {
		// Extract session_id from path: /oauth/cb/{session_id}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 {
			http.Error(w, "missing session_id", http.StatusBadRequest)
			return
		}
		sessionID := parts[2]
		params := make(map[string]string)
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
		if hub.DeliverOAuthCallback(sessionID, params) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!DOCTYPE html><html><body><h2>✅ Authorization complete</h2><p>You can close this tab and return to your chat.</p></body></html>`)
		} else {
			http.Error(w, "unknown or expired session", http.StatusNotFound)
		}
	})
	poeAddr := os.Getenv("POE_HTTP_ADDR")
	if poeAddr == "" {
		poeAddr = httpAddr
	}
	poeSrv := &http.Server{Addr: poeAddr, Handler: poeMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("relay: poe http on %s", poeAddr)
		if err := poeSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("poe http: %v", err)
		}
	}()

	// Agent websocket on agentPort.
	agentMux := http.NewServeMux()
	agentMux.HandleFunc("/ws", hub.HandleAgentWS)
	agentSrv := &http.Server{Addr: ":" + agentPort, Handler: agentMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("relay: agent ws on :%s", agentPort)
		if err := agentSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("agent ws: %v", err)
		}
	}()

	// Shutdown.
	<-sigCh
	log.Printf("relay: shutting down")
	shutCtx, shutCancel := context.WithTimeout(ctx, 5*time.Second)
	defer shutCancel()
	_ = poeSrv.Shutdown(shutCtx)
	_ = agentSrv.Shutdown(shutCtx)
}

// runAgent starts agent mode: connects to relay via ws, bridges MCP stdio.
func runAgent() {
	log.Printf("poe-bridge %s agent mode", version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	relayURL := os.Getenv("POE_RELAY_URL")
	if relayURL == "" {
		// Check args: --agent ws://host:port
		if len(os.Args) > 2 {
			relayURL = os.Args[2]
		}
	}
	if relayURL == "" {
		log.Fatal("agent: POE_RELAY_URL or --agent <url> required")
	}

	convID := os.Getenv("POE_CONV_ID")

	ag, err := agentPkg.Connect(ctx, agentPkg.Config{
		RelayURL: relayURL,
		ConvID:   convID,
	})
	if err != nil {
		log.Fatalf("agent connect: %v", err)
	}
	defer ag.Close()

	// MCP server for fir.
	rt := router.New()
	notif := mcpnotify.NewNotifier()
	mcpSrv := newMCPServer(rt)

	// When relay delivers a query, forward to fir via MCP channel notification.
	// History from Poe's query[] is passed as meta["history"] — fir decides
	// whether and how to use it.
	firstQuery := true
	ag.OnQuery = func(msg relayPkg.RelayMsg) {
		// Include conversation history only on first query (bootstraps the
		// new fir session with context). Subsequent queries arrive in a
		// session that already has the conversation.
		var history string
		if firstQuery && len(msg.Query) > 0 {
			firstQuery = false
			var messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(msg.Query, &messages); err == nil && len(messages) > 1 {
				history = "\n<conversation_history>\n"
				for _, m := range messages[:len(messages)-1] {
					history += fmt.Sprintf("[%s]: %s\n", m.Role, m.Content)
				}
				history += "</conversation_history>\n"
			}
		} else {
			firstQuery = false
		}

		content := fmt.Sprintf("<poe message_id=\"%s\" conversation_id=\"%s\" user_id=\"%s\">\n%s%s\n</poe>",
			msg.MessageID, msg.ConvID, msg.UserID, history, msg.Content)

		meta := map[string]any{
			"source":          "poe",
			"user_id":         msg.UserID,
			"conversation_id": msg.ConvID,
			"message_id":      msg.MessageID,
		}

		_ = notif.SendChannel(ctx, mcpnotify.ChannelMessage{
			Content: content,
			Meta:    meta,
		})
	}

	// Track registered conv_ids to avoid double-registration.
	registeredConvs := &sync.Map{}

	// When relay broadcasts a pending conv, attempt to claim it (race gate).
	// If claim succeeds, post a channel notification so the LLM/skill can
	// spawn a new fir agent. The bridge does NOT spawn — that's the LLM's
	// job, decoupling the harness from the relay.
	ag.OnPending = func(msg relayPkg.RelayMsg) {
		if convID != "" {
			return // dedicated agent — ignore pending for other convs
		}
		if _, loaded := registeredConvs.LoadOrStore(msg.ConvID, true); loaded {
			return // already claimed
		}

		// Race gate: claim the conv.
		log.Printf("[agent] claiming conv=%s", msg.ConvID)
		resp, err := ag.RegisterSync(msg.ConvID, true)
		if err != nil || resp.Type != "register_ok" {
			if err != nil {
				log.Printf("[agent] claim failed for conv=%s: %v", msg.ConvID, err)
			} else {
				log.Printf("[agent] claim rejected for conv=%s: %s", msg.ConvID, resp.Reason)
			}
			registeredConvs.Delete(msg.ConvID)
			return
		}

		// Spawn a dedicated agent directly.
		mcpJSON, _ := json.Marshal(map[string]any{
			"mcpServers": map[string]any{
				"poe": map[string]any{
					"command": "poe-bridge",
					"args":    []string{"--agent", relayURL},
					"env":     map[string]string{"POE_CONV_ID": msg.ConvID},
				},
			},
		})
		cmd := exec.CommandContext(ctx, "spawn-poe-agent", msg.ConvID, string(mcpJSON))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[agent] spawn failed for conv=%s: %v\n%s", msg.ConvID, err, out)
		} else {
			log.Printf("[agent] spawned agent for conv=%s", msg.ConvID)
		}
	}

	// Override the reply tool: instead of using the local router, send
	// replies back to the relay.
	mcpSrv = newMCPServerWithRelay(ag)

	transport := notif.Wrap(&mcp.StdioTransport{})

	go func() {
		<-sigCh
		cancel()
		ag.Close()
	}()

	if err := mcpSrv.Run(ctx, transport); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp: %v", err)
	}
}

// newMCPServerWithRelay creates an MCP server where the reply tool sends
// chunks to the relay agent instead of a local router.
func newMCPServerWithRelay(ag *agentPkg.Agent) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name: "poe-bridge", Version: version,
	}, &mcp.ServerOptions{
		Capabilities: func() *mcp.ServerCapabilities {
			c := &mcp.ServerCapabilities{}
			c.AddExtension("claude/channel", map[string]any{})
			return c
		}(),
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ping",
		Description: "Returns pong and the bridge version.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ pingArgs) (*mcp.CallToolResult, any, error) {
		return pingResult(), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reply",
		Description: "Send a reply chunk to the Poe user. Set final=true on the last chunk.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args replyArgs) (*mcp.CallToolResult, any, error) {
		if args.MessageID == "" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "reply: message_id required"}}}, nil, nil
		}
		if err := ag.Reply(args.MessageID, args.Text, args.Final); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "reply: " + err.Error()}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})

	return srv
}
// runV1 is the original single-bridge mode (no relay).
func runV1() {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background update check — logs if a newer version is available.
	go selfupdate.LogIfAvailable(ctx, version)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Shared state: router (bridging reply tool → SSE) and notifier
	// (bridging poe handler → fir channel notifications).
	rt := router.New()
	notif := mcpnotify.NewNotifier()

	// Access control: if POE_STATE_DIR is set, load or create access.json
	// there for pairing. If unset, all users are allowed (dev mode).
	var acl *access.Store
	if stateDir := os.Getenv("POE_STATE_DIR"); stateDir != "" {
		var err error
		acl, err = access.NewStore(stateDir)
		if err != nil {
			log.Fatalf("access store: %v", err)
		}
		log.Printf("access store loaded from %s (%d allowed)", stateDir, len(acl.AllowFrom()))
	} else {
		log.Printf("POE_STATE_DIR not set — all users allowed (dev mode)")
	}

	// Permission-request queue: uses POE_STATE_DIR for persistence if set.
	var pq *permq.Queue
	if stateDir := os.Getenv("POE_STATE_DIR"); stateDir != "" {
		pq = permq.New(stateDir)
	} else {
		pq = permq.New("")
	}

	botName := os.Getenv("POE_BOT_NAME")
	poeHandler := &poe.Handler{
		AccessKey: os.Getenv("POE_ACCESS_KEY"),
		BotName:   botName,
		OnQuery:   newOnQuery(rt, notif, botName, acl, pq),
	}

	handler := newHTTPHandler(poeHandler)

	// If TS_HOSTNAME is set (and either TS_AUTHKEY or existing tsnet state
	// exists), start a Funnel listener for public HTTPS. Otherwise fall back
	// to plain HTTP on httpAddr.
	var httpSrv *http.Server
	var funnelLn *funnel.Listener
	if tsHostname := os.Getenv("TS_HOSTNAME"); tsHostname != "" {
		stateDir := os.Getenv("POE_STATE_DIR")
		if stateDir == "" {
			stateDir = "."
		}
		var err error
		funnelLn, err = funnel.Listen(ctx, funnel.Config{
			Hostname: tsHostname,
			StateDir: filepath.Join(stateDir, "tsnet"),
		})
		if err != nil {
			log.Fatalf("funnel: %v", err)
		}
		go func() {
			if err := funnelLn.Serve(handler); err != nil {
				log.Printf("funnel serve error: %v", err)
			}
		}()
		// Also start plain HTTP for local health checks.
		httpSrv = newHTTPServer(httpAddr, handler)
		go func() {
			log.Printf("http (local) listening on %s", httpAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("http server error: %v", err)
			}
		}()
	} else {
		httpSrv = newHTTPServer(httpAddr, handler)
		go func() {
			log.Printf("http listening on %s", httpAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("http server error: %v", err)
			}
		}()
	}

	mcpSrv := newMCPServer(rt)
	_ = installShutdown(ctx, cancel, httpSrv, sigCh)

	// If funnel is running, shut it down on context cancel too.
	if funnelLn != nil {
		go func() {
			<-ctx.Done()
			funnelLn.Close()
		}()
	}

	// Wrap the stdio transport so the notifier captures the live Connection
	// and can send custom JSON-RPC notifications on it.
	transport := notif.Wrap(&mcp.StdioTransport{})

	if err := mcpSrv.Run(ctx, transport); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp stdio server error: %v", err)
	}
	log.Printf("poe-bridge exited cleanly")
}
