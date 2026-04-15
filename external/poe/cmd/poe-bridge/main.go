// Command poe-bridge is a Poe server-bot ↔ fir MCP channel bridge.
//
// Two modes:
//   - --relay: Poe HTTP frontend + agent websocket listener
//   - --agent: connects to relay via ws, bridges MCP stdio to fir
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/external/poe/internal/access"
	agentPkg "github.com/kfet/fir/external/poe/internal/agent"
	"github.com/kfet/fir/external/poe/internal/mcpnotify"
	"github.com/kfet/fir/external/poe/internal/poe"
	relayPkg "github.com/kfet/fir/external/poe/internal/relay"
	"github.com/kfet/fir/external/poe/internal/selfupdate"
	"github.com/kfet/fir/external/poe/scripts"
)

const (
	version  = "0.3.0"
	httpAddr = ":8080"
)

// pingArgs is the empty input schema for the ping tool.
type pingArgs struct{}

// replyArgs is the input schema for the `reply` MCP tool that fir calls
// to stream chunks back to a live Poe query.
type replyArgs struct {
	MessageID string `json:"message_id" jsonschema:"the Poe message_id from the inbound channel notification"`
	Text      string `json:"text" jsonschema:"text chunk to append to the reply; may be empty on a final=true call"`
	Final     bool   `json:"final,omitempty" jsonschema:"set true to mark the last chunk; closes the SSE stream on the Poe side"`
	Replace   bool   `json:"replace,omitempty" jsonschema:"if true, replace all prior text instead of appending"`
	Error     bool   `json:"error,omitempty" jsonschema:"if true, emit as error event (shows error UI with retry button)"`
	ErrorType string `json:"error_type,omitempty" jsonschema:"error type: user_caused_error or user_message_too_long"`
}

func pingResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("pong from poe-bridge %s", version)},
		},
	}
}

// writeChunkSSE emits the correct SSE event for a relay ReplyChunk.
func writeChunkSSE(sse *poe.SSEWriter, c relayPkg.ReplyChunk) error {
	if c.IsError {
		data := map[string]any{"allow_retry": true, "text": c.Text}
		if c.ErrorType != "" {
			data["error_type"] = c.ErrorType
		}
		return sse.WriteEvent("error", data)
	}
	if c.Replace {
		return sse.WriteEvent("replace_response", map[string]any{"text": c.Text})
	}
	if c.Text != "" {
		return sse.WriteEvent("text", map[string]any{"text": c.Text})
	}
	return nil
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

	if len(os.Args) > 1 && os.Args[1] == "--relay" {
		runRelay()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--agent" {
		runAgent()
		return
	}

	fmt.Fprintf(os.Stderr, "poe-bridge %s\nUsage: poe-bridge --relay | --agent [relay-url]\n", version)
	os.Exit(1)
}

// --- relay mode -----------------------------------------------------------

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
		OnQuery:   newRelayOnQuery(hub, acl),
	}

	// Poe HTTP mux.
	poeMux := http.NewServeMux()
	poeMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "poe-bridge %s relay ok\n", version)
	})
	poeMux.Handle("/poe", poeHandler)
	// OAuth callback route.
	poeMux.HandleFunc("/oauth/cb/", func(w http.ResponseWriter, r *http.Request) {
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

	// Agent websocket listener.
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

// newRelayOnQuery returns the OnQuery hook for relay mode.
func newRelayOnQuery(hub *relayPkg.Hub, acl *access.Store) func(context.Context, *poe.QueryRequest, *poe.SSEWriter) error {
	return func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
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
			if err := writeChunkSSE(sse, c); err != nil {
				return err
			}
			if c.Final {
				return sse.WriteEvent("done", map[string]any{})
			}
		}
		return sse.WriteEvent("done", map[string]any{})
	}
}

// --- agent mode -----------------------------------------------------------

func runAgent() {
	log.Printf("poe-bridge %s agent mode", version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	relayURL := os.Getenv("POE_RELAY_URL")
	if relayURL == "" {
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
		OnConnect: func() {
			log.Printf("[bridge] relay connected")
		},
		OnDisconnect: func(err error) {
			log.Printf("[bridge] relay disconnected (will reconnect)")
		},
	})
	if err != nil {
		// Connect only returns error for truly fatal problems (not dial failures).
		log.Fatalf("agent connect: %v", err)
	}
	defer ag.Close()

	notif := mcpnotify.NewNotifier()

	ag.OnRegisterRejected = func(convID, reason string) {
		log.Printf("[bridge] register rejected for conv=%s: %s — this agent is orphaned", convID, reason)
		// Notify fir so the LLM knows it's been replaced.
		_ = notif.SendChannel(ctx, mcpnotify.ChannelMessage{
			Content: fmt.Sprintf("[system] This agent's registration for conversation %s was rejected (reason: %s). Another agent has taken over this conversation. This session is now idle.", convID, reason),
			Meta: map[string]any{
				"source": "poe",
				"type":   "register_rejected",
				"reason": reason,
			},
		})
	}

	// When relay delivers a query, forward to fir via MCP channel notification.
	ag.OnQuery = func(msg relayPkg.RelayMsg) {
		// Build content with attachment info appended.
		contentBody := msg.Content
		attachments := extractAttachments(msg.Query)

		// Separate images (download for vision) from documents (inline text).
		var images []mcpnotify.ChannelImage
		var nonImageAtts []poeAttachment
		for _, a := range attachments {
			if isImageMIME(a.ContentType) {
				img, err := downloadImage(a.URL, a.ContentType, a.Name)
				if err != nil {
					log.Printf("[agent] download image %s: %v", a.Name, err)
					nonImageAtts = append(nonImageAtts, a) // fall back to link
				} else {
					images = append(images, img)
				}
			} else {
				nonImageAtts = append(nonImageAtts, a)
			}
		}
		if len(nonImageAtts) > 0 {
			contentBody += "\n\n" + formatAttachments(nonImageAtts)
		}

		content := fmt.Sprintf("<poe message_id=\"%s\" conversation_id=\"%s\" user_id=\"%s\">\n%s\n</poe>",
			msg.MessageID, msg.ConvID, msg.UserID, contentBody)

		meta := map[string]any{
			"source":          "poe",
			"user_id":         msg.UserID,
			"conversation_id": msg.ConvID,
			"message_id":      msg.MessageID,
		}
		if len(msg.Query) > 0 {
			meta["history"] = msg.Query
		}

		_ = notif.SendChannel(ctx, mcpnotify.ChannelMessage{
			Content: content,
			Meta:    meta,
			Images:  images,
		})
	}

	// Track registered conv_ids to avoid double-registration.
	registeredConvs := &sync.Map{}

	// When relay broadcasts a pending conv, attempt to claim + spawn.
	ag.OnPending = func(msg relayPkg.RelayMsg) {
		if convID != "" {
			return // dedicated agent — ignore pending for other convs
		}
		if _, loaded := registeredConvs.LoadOrStore(msg.ConvID, true); loaded {
			return
		}

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

		mcpJSON, _ := json.Marshal(map[string]any{
			"mcpServers": map[string]any{
				"poe": map[string]any{
					"command": "poe-bridge",
					"args":    []string{"--agent", relayURL},
					"env":     map[string]string{"POE_CONV_ID": msg.ConvID},
				},
			},
		})
		spawnBin, err := scripts.SpawnPoeAgentPath()
		if err != nil {
			log.Printf("[agent] spawn script extract failed: %v", err)
			registeredConvs.Delete(msg.ConvID)
			return
		}
		cmd := exec.CommandContext(ctx, spawnBin, msg.ConvID, string(mcpJSON))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[agent] spawn failed for conv=%s: %v\n%s", msg.ConvID, err, out)
		} else {
			log.Printf("[agent] spawned agent for conv=%s", msg.ConvID)
		}
	}

	mcpSrv := newAgentMCPServer(ag)
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

// newAgentMCPServer creates an MCP server where the reply tool sends
// chunks to the relay via the agent connection.
func newAgentMCPServer(ag *agentPkg.Agent) *mcp.Server {
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
		if err := ag.Reply(args.MessageID, args.Text, args.Final, args.Replace, args.Error, args.ErrorType); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "reply: " + err.Error()}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})

	return srv
}

// poeAttachment matches the attachment shape in Poe's ProtocolMessage JSON.
type poeAttachment struct {
	URL           string `json:"url"`
	ContentType   string `json:"content_type"`
	Name          string `json:"name"`
	ParsedContent string `json:"parsed_content,omitempty"`
	IsInline      bool   `json:"is_inline,omitempty"`
}

// poeQueryMsg is used to extract attachments from the raw query JSON array.
type poeQueryMsg struct {
	Role        string          `json:"role"`
	Attachments []poeAttachment `json:"attachments,omitempty"`
}

// extractAttachments pulls attachments from the last user message in the raw query JSON.
func extractAttachments(queryJSON json.RawMessage) []poeAttachment {
	if len(queryJSON) == 0 {
		return nil
	}
	var msgs []poeQueryMsg
	if err := json.Unmarshal(queryJSON, &msgs); err != nil {
		return nil
	}
	// Only look at the last user message (the one that triggered this query).
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].Attachments) > 0 {
			return msgs[i].Attachments
		}
	}
	return nil
}

// formatAttachments renders attachments as markdown for the AI context.
// Images become markdown image links; documents with parsed_content are
// included inline; other files are linked.
func formatAttachments(atts []poeAttachment) string {
	var b strings.Builder
	b.WriteString("**Attachments:**\n")
	for _, a := range atts {
		if isImageMIME(a.ContentType) {
			// Markdown image — the AI can see this via vision.
			b.WriteString(fmt.Sprintf("![%s](%s)\n", a.Name, a.URL))
		} else if a.ParsedContent != "" {
			// Document with pre-extracted text.
			b.WriteString(fmt.Sprintf("\n📄 **%s** (%s):\n```\n%s\n```\n", a.Name, a.ContentType, a.ParsedContent))
		} else {
			// Other file — link it.
			b.WriteString(fmt.Sprintf("📎 [%s](%s) (%s)\n", a.Name, a.URL, a.ContentType))
		}
	}
	return b.String()
}

// isImageMIME returns true if the MIME type is an image type.
func isImageMIME(ct string) bool {
	return strings.HasPrefix(ct, "image/")
}

// downloadImage fetches an image URL and returns it as a base64-encoded ChannelImage.
func downloadImage(url, mimeType, name string) (mcpnotify.ChannelImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return mcpnotify.ChannelImage{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mcpnotify.ChannelImage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return mcpnotify.ChannelImage{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Cap at 20MB to avoid OOM on huge images.
	const maxImageBytes = 20 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return mcpnotify.ChannelImage{}, err
	}

	// Use content-type from response if available, fall back to provided.
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = mimeType
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return mcpnotify.ChannelImage{
		MimeType: ct,
		Data:     encoded,
		Name:     name,
	}, nil
}
