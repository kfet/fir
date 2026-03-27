package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Agent interface implementation
// ============================================================================

func (pa *firAgent) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	initStart := time.Now()
	firlog.Info("acp initialize: start")

	pa.mu.Lock()
	pa.clientCaps = params.ClientCapabilities
	pa.mu.Unlock()

	// Build auth methods from the global agent dir config.
	t0 := time.Now()
	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	firlog.Info("acp initialize: auth storage created", "elapsed_ms", time.Since(t0).Milliseconds())

	t0 = time.Now()
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	firlog.Info("acp initialize: model registry created", "elapsed_ms", time.Since(t0).Milliseconds())

	t0 = time.Now()
	authMethods := buildAuthMethods(authStorage, modelRegistry, params.ClientCapabilities)
	firlog.Info("acp initialize: auth methods built", "elapsed_ms", time.Since(t0).Milliseconds())

	pa.mu.Lock()
	pa.authMethods = authMethods
	pa.authStorage = authStorage
	pa.mu.Unlock()

	firlog.Info("acp initialize: done", "total_ms", time.Since(initStart).Milliseconds())
	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentInfo:       &acpsdk.Implementation{Name: "fir", Version: version},
		AgentCapabilities: acpsdk.AgentCapabilities{
			PromptCapabilities: acpsdk.PromptCapabilities{
				Image:           true,
				EmbeddedContext: true,
			},
			McpCapabilities: acpsdk.McpCapabilities{
				Http: true,
				Sse:  true,
			},
		},
		AuthMethods: toSDKAuthMethods(authMethods),
	}, nil
}

func (pa *firAgent) Authenticate(ctx context.Context, req acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return pa.handleAuthenticate(ctx, req)
}

func (pa *firAgent) NewSession(ctx context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	newSessionStart := time.Now()
	sessionID := uuid.New().String()
	firlog.Info("acp new session: start", "sessionID", sessionID)
	cwd := os.Getenv("PWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if params.Cwd != "" {
		cwd = params.Cwd
	}

	// Merge project-level configs with request-level configs.
	// Request-level entries take precedence over project-level ones.
	t0 := time.Now()
	var mcpConfigs map[string]mcp.ServerConfig
	if !pa.options.NoMCP {
		mcpConfigs = loadProjectMCPConfigs(cwd, pa.options.MCPConfig)
	}
	firlog.Info("acp new session: loaded project MCP configs", "elapsed_ms", time.Since(t0).Milliseconds(), "count", len(mcpConfigs))
	if mcpConfigs == nil && len(params.McpServers) > 0 {
		mcpConfigs = make(map[string]mcp.ServerConfig)
	}
	for _, mcpServer := range params.McpServers {
		switch {
		case mcpServer.Stdio != nil:
			envs := map[string]string{}
			for _, v := range mcpServer.Stdio.Env {
				envs[v.Name] = v.Value
			}
			mcpConfigs[mcpServer.Stdio.Name] = mcp.ServerConfig{
				Command: mcpServer.Stdio.Command,
				Args:    mcpServer.Stdio.Args,
				Env:     envs,
			}
		case mcpServer.Http != nil:
			if len(mcpServer.Http.Headers) > 0 {
				fmt.Fprintf(os.Stderr, "fir: warning: MCP server %q specifies HTTP headers which are not yet supported; headers will be ignored\n", mcpServer.Http.Name)
			}
			mcpConfigs[mcpServer.Http.Name] = mcp.ServerConfig{
				Transport: "streamable",
				URL:       mcpServer.Http.Url,
			}
		case mcpServer.Sse != nil:
			if len(mcpServer.Sse.Headers) > 0 {
				fmt.Fprintf(os.Stderr, "fir: warning: MCP server %q specifies HTTP headers which are not yet supported; headers will be ignored\n", mcpServer.Sse.Name)
			}
			mcpConfigs[mcpServer.Sse.Name] = mcp.ServerConfig{
				Transport: "sse",
				URL:       mcpServer.Sse.Url,
			}
		default:
			fmt.Fprintf(os.Stderr, "fir: warning: MCP server has unknown transport; skipping\n")
		}
	}

	t0 = time.Now()
	entry, err := pa.createSession(ctx, sessionID, cwd, mcpConfigs)
	firlog.Info("acp new session: createSession done", "elapsed_ms", time.Since(t0).Milliseconds())
	if err != nil {
		return acpsdk.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	var models *acpsdk.SessionModelState
	if m := entry.session.Model(); m != nil {
		models = BuildModelState(entry.modelRegistry, m)
	}

	firlog.Info("acp new session: done", "total_ms", time.Since(newSessionStart).Milliseconds())
	return acpsdk.NewSessionResponse{
		SessionId: acpsdk.SessionId(sessionID),
		Models:    models,
	}, nil
}

func (pa *firAgent) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	pa.mu.Lock()
	entry, ok := pa.sessions[string(params.SessionId)]
	pa.mu.Unlock()
	if !ok {
		return acpsdk.PromptResponse{}, fmt.Errorf("session not found: %s", params.SessionId)
	}

	// Wait for async extension setup to complete so hooks and event
	// forwarding are wired before any tool calls execute.
	if entry.extReady != nil {
		<-entry.extReady
	}

	text, images := ExtractPromptContent(params.Prompt)

	// Handle slash commands
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(strings.TrimSpace(text))
		command := strings.TrimPrefix(parts[0], "/")
		args := ""
		if len(parts) > 1 {
			args = strings.Join(parts[1:], " ")
		}
		if pa.handleSlashCommand(string(params.SessionId), entry, command, args) {
			return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
		}
	}

	// Regular prompt
	var opts *session.PromptOptions
	if len(images) > 0 {
		opts = &session.PromptOptions{Images: images}
	}
	if err := entry.session.Prompt(text, opts); err != nil {
		errMsg := err.Error()
		// Surface auth errors as AUTH_REQUIRED so the client shows the login UI.
		if strings.Contains(errMsg, "no model selected") ||
			strings.Contains(errMsg, "API key") ||
			strings.Contains(errMsg, "authentication") ||
			strings.Contains(errMsg, "unauthorized") ||
			strings.Contains(errMsg, "401") {
			return acpsdk.PromptResponse{}, acpsdk.NewAuthRequired(map[string]any{"error": errMsg})
		}
		// Return other errors as internal errors so the client displays them.
		return acpsdk.PromptResponse{}, err
	}

	// Clear plan at end of turn so the next turn starts fresh.
	entry.plan.clear()

	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

func (pa *firAgent) Cancel(_ context.Context, params acpsdk.CancelNotification) error {
	pa.mu.Lock()
	entry, ok := pa.sessions[string(params.SessionId)]
	pa.mu.Unlock()
	if !ok {
		return nil
	}
	entry.session.Agent.Abort()
	CleanupPendingBashTerminals(context.Background(), pa.conn, entry.termState, string(params.SessionId))
	CleanupBackgroundTerminals(context.Background(), pa.conn, entry.termState, string(params.SessionId))
	return nil
}

func (pa *firAgent) SetSessionMode(_ context.Context, _ acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

// ============================================================================
// Unstable ACP methods (session/set_model, session/list, session/resume)
// These are handled by rawMethodHandler in conn.go, which calls these methods.
// ============================================================================

func (pa *firAgent) SetSessionModel(_ context.Context, params acpsdk.SetSessionModelRequest) (acpsdk.SetSessionModelResponse, error) {
	pa.mu.Lock()
	entry, ok := pa.sessions[string(params.SessionId)]
	pa.mu.Unlock()
	if !ok {
		return acpsdk.SetSessionModelResponse{}, fmt.Errorf("session not found: %s", params.SessionId)
	}

	provider, modelID, err := ParseModelID(string(params.ModelId))
	if err != nil {
		return acpsdk.SetSessionModelResponse{}, err
	}

	model := entry.modelRegistry.Find(provider, modelID)
	if model == nil {
		return acpsdk.SetSessionModelResponse{}, fmt.Errorf("model not found: %s", params.ModelId)
	}
	entry.session.SetModel(model)
	return acpsdk.SetSessionModelResponse{}, nil
}

// ListSessions handles the session/list method.
// It returns sessions from the caller's working directory.
func (pa *firAgent) ListSessions(_ context.Context, params ListSessionsRequest) (ListSessionsResponse, error) {
	agentDir := resolveAgentDir()

	// When FIR_AGENT_DIR is explicitly set, don't scan legacy dirs.
	var legacyDirs []string
	if os.Getenv("FIR_AGENT_DIR") == "" {
		legacyDirs = []string{session.LegacyFirAgentDir(), session.PiAgentDir()}
	}

	var allSessions []store.SessionListInfo

	if params.Cwd != "" {
		// Scoped to a specific cwd — check primary and legacy dirs.
		sessionDir := store.DefaultSessionDir(agentDir, params.Cwd)
		sessions, err := store.ListSessions(params.Cwd, sessionDir)
		if err != nil {
			return ListSessionsResponse{}, err
		}
		allSessions = sessions
		for _, legacyDir := range legacyDirs {
			legacySessionDir := store.DefaultSessionDir(legacyDir, params.Cwd)
			if legacySessions, err := store.ListSessions(params.Cwd, legacySessionDir); err == nil {
				allSessions = append(allSessions, legacySessions...)
			}
		}
	} else {
		// No cwd filter — enumerate all session directories (primary + legacy).
		allDirs := append([]string{agentDir}, legacyDirs...)
		for _, dir := range allDirs {
			sessionsRoot := filepath.Join(dir, "sessions")
			entries, err := os.ReadDir(sessionsRoot)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				dir := filepath.Join(sessionsRoot, entry.Name())
				sessions, err := store.ListSessions("", dir)
				if err != nil {
					firlog.Debug("session/list: skipping dir", "dir", dir, "err", err)
					continue
				}
				allSessions = append(allSessions, sessions...)
			}
		}
	}

	// Deduplicate sessions that appear in both new and legacy dirs.
	seen := make(map[string]bool)
	deduped := make([]store.SessionListInfo, 0, len(allSessions))
	for _, s := range allSessions {
		if !seen[s.Path] {
			seen[s.Path] = true
			deduped = append(deduped, s)
		}
	}
	allSessions = deduped

	// Sort all sessions by modification time (most recent first).
	sort.Slice(allSessions, func(i, j int) bool {
		return allSessions[i].Modified.After(allSessions[j].Modified)
	})

	infos := make([]SessionInfo, 0, len(allSessions))
	for _, s := range allSessions {
		var title *string
		if s.Name != "" {
			title = &s.Name
		} else if s.FirstMessage != "" {
			msg := s.FirstMessage
			// Collapse whitespace and truncate for UI display.
			msg = strings.Join(strings.Fields(msg), " ")
			runes := []rune(msg)
			if len(runes) > 100 {
				msg = string(runes[:100]) + "…"
			}
			title = &msg
		}
		infos = append(infos, SessionInfo{
			SessionId: s.Path,
			Cwd:       s.Cwd,
			Title:     title,
			UpdatedAt: s.Modified.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return ListSessionsResponse{Sessions: infos}, nil
}

// ResumeSession handles the session/resume method.
// It creates a new AgentSession and switches it to the requested session file.
func (pa *firAgent) ResumeSession(ctx context.Context, params ResumeSessionRequest) (ResumeSessionResponse, error) {
	cwd := params.Cwd
	if cwd == "" {
		cwd = os.Getenv("PWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
	}

	// Validate session path is within the sessions directory to prevent traversal.
	agentDir := resolveAgentDir()
	sessionsDir := store.SessionsDir(agentDir)
	sessionPath, err := filepath.Abs(params.SessionId)
	if err != nil {
		return ResumeSessionResponse{}, fmt.Errorf("invalid session path %q: %w", params.SessionId, err)
	}
	if !IsPathWithinDirectory(sessionPath, sessionsDir) {
		return ResumeSessionResponse{}, fmt.Errorf("invalid session path: must be within sessions directory")
	}

	// Use params.SessionId as the new session's ID so the client can reference it.
	sessionID := params.SessionId

	// Close any existing session with the same ID before creating a new one.
	// Without this, a client retry would overwrite the old session's unsubscribe,
	// extSetup, and agent goroutine, leaking all three.
	pa.mu.Lock()
	if existing, ok := pa.sessions[sessionID]; ok {
		delete(pa.sessions, sessionID)
		pa.mu.Unlock()
		CleanupPendingBashTerminals(ctx, pa.conn, existing.termState, sessionID)
		CleanupBackgroundTerminals(ctx, pa.conn, existing.termState, sessionID)
		if existing.unsubscribe != nil {
			existing.unsubscribe()
		}
		existing.session.Close()
		if existing.extReady != nil {
			<-existing.extReady
		}
		if existing.extSetup != nil {
			existing.extSetup.EmitSessionShutdown()
		}
		if existing.mcpManager != nil {
			_ = existing.mcpManager.Close()
		}
	} else {
		pa.mu.Unlock()
	}

	var resumeMCPConfigs map[string]mcp.ServerConfig
	if !pa.options.NoMCP {
		resumeMCPConfigs = loadProjectMCPConfigs(cwd, pa.options.MCPConfig)
	}
	entry, err := pa.createSession(ctx, sessionID, cwd, resumeMCPConfigs)
	if err != nil {
		return ResumeSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	// Switch to the requested session file.
	if err := entry.session.SwitchSession(sessionPath); err != nil {
		return ResumeSessionResponse{}, fmt.Errorf("switch session: %w", err)
	}

	var models interface{}
	if m := entry.session.Model(); m != nil {
		models = BuildModelState(entry.modelRegistry, m)
	}
	return ResumeSessionResponse{Models: models}, nil
}

// replaySessionHistory pushes the historical messages from a resumed session
// to the ACP client as session update notifications. This allows the client
// to display the full conversation history from previous turns.
func (pa *firAgent) replaySessionHistory(sessionID string, entry *firSession) {
	ctx := entry.session.SessionManager.BuildSessionContext()
	sid := acpsdk.SessionId(sessionID)

	// Track tool calls from assistant messages so we can match them with results.
	type pendingTool struct {
		name string
		args map[string]any
	}
	pendingTools := make(map[string]pendingTool)

	for _, msg := range ctx.Messages {
		switch msg.Role() {
		case "user":
			um := msg.AsUser()
			if um == nil {
				continue
			}
			text := extractUserText(um.Content)
			if text != "" {
				_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
					SessionId: sid,
					Update:    acpsdk.UpdateUserMessageText(text),
				})
			}

		case "assistant":
			am := msg.AsAssistant()
			if am == nil {
				continue
			}
			for _, c := range am.Content {
				if c.IsText() && c.Text != nil {
					_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
						SessionId: sid,
						Update:    acpsdk.UpdateAgentMessageText(c.Text.Text),
					})
				} else if c.IsThinking() && c.Thinking != nil {
					_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
						SessionId: sid,
						Update:    acpsdk.UpdateAgentThoughtText(c.Thinking.Thinking),
					})
				} else if c.IsToolCall() && c.ToolCall != nil {
					tc := c.ToolCall
					pendingTools[tc.ID] = pendingTool{name: tc.Name, args: tc.Arguments}

					var startOpts []acpsdk.ToolCallStartOpt
					startOpts = append(startOpts,
						acpsdk.WithStartKind(MapToolKind(tc.Name)),
						acpsdk.WithStartStatus("completed"),
						acpsdk.WithStartRawInput(tc.Arguments),
					)
					locs := BuildToolLocations(tc.Name, tc.Arguments)
					if len(locs) > 0 {
						startOpts = append(startOpts, acpsdk.WithStartLocations(locs))
					}
					_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
						SessionId: sid,
						Update:    acpsdk.StartToolCall(acpsdk.ToolCallId(tc.ID), BuildToolTitle(tc.Name, tc.Arguments), startOpts...),
					})
				}
			}

		case "toolResult":
			tr := msg.AsToolResult()
			if tr == nil {
				continue
			}
			status := acpsdk.ToolCallStatus("completed")
			if tr.IsError {
				status = "failed"
			}
			var updateOpts []acpsdk.ToolCallUpdateOpt
			updateOpts = append(updateOpts, acpsdk.WithUpdateStatus(status))

			// Build result for raw output.
			resultMap := map[string]any{
				"content": toolResultToContent(tr.Content),
				"isError": tr.IsError,
			}
			if tr.Details != nil {
				resultMap["details"] = tr.Details
			}
			updateOpts = append(updateOpts, acpsdk.WithUpdateRawOutput(resultMap))

			// Build display content using the original tool args if available.
			var argsMap map[string]any
			toolName := tr.ToolName
			if pt, ok := pendingTools[tr.ToolCallID]; ok {
				argsMap = pt.args
				toolName = pt.name
				delete(pendingTools, tr.ToolCallID)
			}
			content, locations := BuildToolCallContent(toolName, argsMap, resultMap, tr.IsError)
			if len(content) > 0 {
				updateOpts = append(updateOpts, acpsdk.WithUpdateContent(content))
			}
			if len(locations) > 0 {
				updateOpts = append(updateOpts, acpsdk.WithUpdateLocations(locations))
			}

			_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
				SessionId: sid,
				Update:    acpsdk.UpdateToolCall(acpsdk.ToolCallId(tr.ToolCallID), updateOpts...),
			})
		}
	}
}

// extractUserText extracts text from a UserMessage content field,
// which may be a string or []UserContentBlock.
func extractUserText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	blocks, ok := content.([]any)
	if !ok {
		return fmt.Sprint(content)
	}
	var parts []string
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "text" {
			if text, ok := m["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// toolResultToContent converts ToolResultContent to a generic slice for serialization.
func toolResultToContent(content []ai.ToolResultContent) []any {
	result := make([]any, 0, len(content))
	for _, c := range content {
		result = append(result, map[string]any{
			"type": c.Type,
			"text": c.Text,
		})
	}
	return result
}

// ============================================================================
// Event handling
// ============================================================================

func (pa *firAgent) handleEvent(sessionID string, entry *firSession, event session.AgentSessionEvent) {
	// Handle session-level events first (no AgentEvent required).
	switch event.Type {
	case "plan_update":
		entry.plan.update(event.PlanEntries)
		return
	case "auto_compaction_end":
		if event.ErrorMessage != "" {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("⚠️ Compaction failed: %s", event.ErrorMessage))
		}
		return
	}

	if event.AgentEvent == nil {
		return
	}
	ev := event.AgentEvent

	switch ev.Type {
	case agent.EventMessageUpdate:
		if ev.AssistantMessageEvent == nil {
			return
		}
		msg := ev.AssistantMessageEvent
		switch msg.Type {
		case "text_delta":
			_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
				SessionId: acpsdk.SessionId(sessionID),
				Update:    acpsdk.UpdateAgentMessageText(msg.Delta),
			})
		case "thinking_delta":
			_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
				SessionId: acpsdk.SessionId(sessionID),
				Update:    acpsdk.UpdateAgentThoughtText(msg.Delta),
			})
		}

	case agent.EventToolExecutionStart:
		argsMap, _ := ev.Args.(map[string]any)
		if argsMap != nil {
			entry.pendingArgs.Store(ev.ToolCallID, argsMap)
		}

		var startOpts []acpsdk.ToolCallStartOpt
		startOpts = append(startOpts,
			acpsdk.WithStartKind(MapToolKind(ev.ToolName)),
			acpsdk.WithStartStatus("in_progress"),
			acpsdk.WithStartRawInput(argsMap),
		)

		locs := BuildToolLocations(ev.ToolName, argsMap)
		if len(locs) > 0 {
			startOpts = append(startOpts, acpsdk.WithStartLocations(locs))
		}
		initContent := BuildToolInitialContent(ev.ToolName, argsMap)
		if len(initContent) > 0 {
			startOpts = append(startOpts, acpsdk.WithStartContent(initContent))
		}

		_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
			SessionId: acpsdk.SessionId(sessionID),
			Update:    acpsdk.StartToolCall(acpsdk.ToolCallId(ev.ToolCallID), BuildToolTitle(ev.ToolName, argsMap), startOpts...),
		})

	case agent.EventToolExecutionEnd:
		var argsMap map[string]any
		if v, ok := entry.pendingArgs.LoadAndDelete(ev.ToolCallID); ok {
			argsMap, _ = v.(map[string]any)
		}

		// Check if ACP client terminal was used (skip text content to avoid duplication)
		entry.termState.mu.Lock()
		usedAcpTerminal := false
		if _, ok := entry.termState.pendingBashTerminals[ev.ToolCallID]; ok {
			usedAcpTerminal = true
			delete(entry.termState.pendingBashTerminals, ev.ToolCallID)
		}
		entry.termState.mu.Unlock()

		status := acpsdk.ToolCallStatus("completed")
		if ev.IsError {
			status = "failed"
		}

		var updateOpts []acpsdk.ToolCallUpdateOpt
		updateOpts = append(updateOpts, acpsdk.WithUpdateStatus(status), acpsdk.WithUpdateRawOutput(ev.Result))

		if !usedAcpTerminal {
			content, locations := BuildToolCallContent(ev.ToolName, argsMap, ev.Result, ev.IsError)
			if len(content) > 0 {
				updateOpts = append(updateOpts, acpsdk.WithUpdateContent(content))
			}
			if len(locations) > 0 {
				updateOpts = append(updateOpts, acpsdk.WithUpdateLocations(locations))
			}
		}

		_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
			SessionId: acpsdk.SessionId(sessionID),
			Update:    acpsdk.UpdateToolCall(acpsdk.ToolCallId(ev.ToolCallID), updateOpts...),
		})

	case agent.EventMessageEnd:
		// Surface inference errors (e.g. Bedrock API failures) to the ACP client.
		// errorAssistantMessage() produces a message with empty Content and a non-empty
		// ErrorMessage; without this handler those errors are silently dropped.
		if ev.Message == nil {
			return
		}
		msg := ev.Message.AsAssistant()
		if msg == nil || msg.ErrorMessage == "" {
			return
		}
		errText := msg.ErrorMessage
		// Distinguish aborted (user cancel) from real errors to avoid noise.
		if msg.StopReason == ai.StopReasonAborted {
			return
		}
		pa.sendAgentMessage(sessionID, fmt.Sprintf("⚠️ %s", errText))
	}
}

