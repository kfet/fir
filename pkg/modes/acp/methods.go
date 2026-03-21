package acp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/auth"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
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
	mcpConfigs := loadProjectMCPConfigs(cwd)
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
	cwd := params.Cwd
	if cwd == "" {
		cwd = os.Getenv("PWD")
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
	}

	agentDir := resolveAgentDir()
	sessionDir := store.DefaultSessionDir(agentDir, cwd)
	sessions, err := store.ListSessions(cwd, sessionDir)
	if err != nil {
		return ListSessionsResponse{}, err
	}

	infos := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		var title *string
		if s.Name != "" {
			title = &s.Name
		} else if s.FirstMessage != "" {
			title = &s.FirstMessage
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

	entry, err := pa.createSession(ctx, sessionID, cwd, loadProjectMCPConfigs(cwd))
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

// ============================================================================
// Slash commands
// ============================================================================

func (pa *firAgent) handleSlashCommand(sessionID string, entry *firSession, command, args string) bool {
	switch command {
	case "compact":
		if _, err := entry.session.RunCompaction(context.Background(), args); err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Compaction failed: %v", err))
		} else {
			// Auto-resume if pending work (unanswered user message or tool result)
			if entry.session.HasPendingWork() {
				go func() { _ = entry.session.Agent.Continue() }()
				pa.sendAgentMessage(sessionID, "Session compacted successfully. Resuming.")
			} else {
				pa.sendAgentMessage(sessionID, "Session compacted successfully.")
			}
		}
		return true

	case "resume":
		if args == "" {
			sessionDir := store.DefaultSessionDir(entry.agentDir, entry.cwd)
			sessions, _ := store.ListSessions(entry.cwd, sessionDir)
			if len(sessions) > 10 {
				sessions = sessions[:10]
			}
			entry.resumeMu.Lock()
			entry.lastResumeList = sessions
			entry.resumeMu.Unlock()
			var lines []string
			for i, s := range sessions {
				name := s.Name
				if name == "" {
					name = s.FirstMessage
				}
				if name == "" {
					name = "(unnamed)"
				}
				lines = append(lines, fmt.Sprintf("%d. %s (%s)", i+1, name, s.Path))
			}
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Available sessions (top 10):\n%s\n\nTo resume: /resume <number> or /resume <path>", strings.Join(lines, "\n")))
		} else {
			pa.handleResumeArg(sessionID, entry, args)
		}
		return true

	case "continue":
		sessionDir := store.DefaultSessionDir(entry.agentDir, entry.cwd)
		sessions, _ := store.ListSessions(entry.cwd, sessionDir)
		if len(sessions) == 0 {
			pa.sendAgentMessage(sessionID, "No sessions available to continue.")
			return true
		}
		sessionsDir := store.SessionsDir(entry.agentDir)
		if !IsPathWithinDirectory(sessions[0].Path, sessionsDir) {
			pa.sendAgentMessage(sessionID, "Invalid session path: must be within sessions directory")
			return true
		}
		if err := entry.session.SwitchSession(sessions[0].Path); err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to continue session: %v", err))
		} else {
			name := sessions[0].Name
			if name == "" {
				name = sessions[0].FirstMessage
			}
			if name == "" {
				name = sessions[0].Path
			}
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Continued session: %s", name))
			pa.replaySessionHistory(sessionID, entry)
		}
		return true

	case "name":
		if args == "" {
			pa.sendAgentMessage(sessionID, "Usage: /name <new name>")
		} else {
			entry.session.SessionManager.AppendSessionInfo(args)
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Session renamed to: %s", args))
		}
		return true

	case "session":
		stats := entry.session.GetSessionStats()
		name := entry.session.SessionManager.GetSessionName()
		info := "**Session Info**\n\n"
		info += fmt.Sprintf("**Version:** %s\n", version)
		info += "**Mode:** acp\n"
		if bin, err := os.Executable(); err == nil {
			info += fmt.Sprintf("**Binary:** %s\n", bin)
		}
		if name != "" {
			info += fmt.Sprintf("**Name:** %s\n", name)
		}
		info += fmt.Sprintf("**ID:** %s\n", stats.SessionID)
		if entry.extSetup != nil && entry.extSetup.Manager != nil {
			enabled := entry.extSetup.Manager.EnabledExtensionNames()
			if len(enabled) > 0 {
				info += fmt.Sprintf("**Extensions:** %s\n", strings.Join(enabled, ", "))
			}
		}
		if model := entry.session.Model(); model != nil {
			info += fmt.Sprintf("**Model:** %s\n", model.ID)
			info += fmt.Sprintf("**Provider:** %s\n", model.Provider)
		}
		if mcpCfg, err := mcp.LoadDefaultConfigs(entry.cwd); err == nil && len(mcpCfg.MCPServers) > 0 {
			var names []string
			for n := range mcpCfg.MCPServers {
				names = append(names, n)
			}
			sort.Strings(names)
			info += fmt.Sprintf("**MCP Servers:** %s\n", strings.Join(names, ", "))
		}
		info += "\n"
		info += fmt.Sprintf("**Messages**\n- User: %d\n- Assistant: %d\n- Tool Calls: %d\n- Total: %d\n\n",
			stats.UserMessages, stats.AssistantMessages, stats.ToolCalls, stats.TotalMessages)
		info += fmt.Sprintf("**Tokens**\n- Input: %d\n- Output: %d\n- Total: %d\n",
			stats.Tokens.Input, stats.Tokens.Output, stats.Tokens.Total)
		if stats.Cost > 0 {
			info += fmt.Sprintf("\n**Cost**: $%.4f", stats.Cost)
		}
		pa.sendAgentMessage(sessionID, info)
		return true

	case "changelog":
		entries := session.GetChangelogEntries()
		if len(entries) == 0 {
			pa.sendAgentMessage(sessionID, "No changelog entries found.")
		} else {
			// Display oldest-first so newest appears at the bottom (most visible).
			var texts []string
			for i := len(entries) - 1; i >= 0; i-- {
				texts = append(texts, entries[i].Content)
			}
			pa.sendAgentMessage(sessionID, "**What's New**\n\n"+strings.Join(texts, "\n\n"))
		}
		return true

	case "share":
		go pa.performShare(sessionID, entry)
		return true

	case "export":
		go func() {
			filePath, err := entry.session.ExportToHTML(args)
			if err != nil {
				pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to export session: %v", err))
				return
			}
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Session exported to: %s", filePath))
		}()
		return true

	case "login":
		pa.handleLogin(sessionID, entry, args)
		return true

	case "logout":
		pa.handleLogout(sessionID, entry, args)
		return true

	case "reload":
		if err := entry.session.Reload(); err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Reload failed: %v", err))
		} else {
			if entry.extSetup != nil {
				if entry.extSetup.Manager != nil {
					entry.extSetup.Manager.SetAllowedNames(resolveEnabledExtensions(pa.options.EnabledExtensions, entry.settingsManager))
				}
				_ = entry.extSetup.Reload(context.Background())
			}
			pa.sendAvailableCommands(sessionID)
			pa.sendAgentMessage(sessionID, "Reload completed successfully.")
		}
		return true

	case "skills":
		pa.handleSkillsCommand(sessionID, entry, args)
		return true

	default:
		// Check extension slash commands first (mirrors interactive mode ordering).
		if entry.extSetup != nil && entry.extSetup.Manager != nil {
			for _, ec := range entry.extSetup.Manager.GetCommands() {
				if ec.Spec.Name == command {
					var argList []string
					if args != "" {
						argList = strings.Fields(args)
					}
					result, err := entry.extSetup.Manager.DispatchCommand(command, argList, 0)
					if err != nil {
						pa.sendAgentMessage(sessionID, fmt.Sprintf("Extension command /%s failed: %v", command, err))
					} else if result.Message != "" {
						pa.sendAgentMessage(sessionID, result.Message)
					}
					return true
				}
			}
		}

		// Check prompt templates
		templates, _ := entry.session.ResourceLoader().GetPrompts()
		for _, t := range templates {
			if t.Name == command {
				fullCmd := "/" + command
				if args != "" {
					fullCmd += " " + args
				}
				expanded := resources.ExpandPromptTemplate(fullCmd, templates)
				if expanded != fullCmd {
					_ = entry.session.Prompt(expanded)
				}
				return true
			}
		}

		// Check skill commands
		if strings.HasPrefix(command, "skill:") && (entry.settingsManager == nil || entry.settingsManager.GetEnableSkillCommands()) {
			skillName := strings.TrimPrefix(command, "skill:")
			skills, _ := entry.session.ResourceLoader().GetSkills()
			for _, s := range skills {
				if s.Name == skillName {
					_ = entry.session.Prompt(fmt.Sprintf("/skill:%s %s", s.Name, args))
					return true
				}
			}
		}

		return false
	}
}

func (pa *firAgent) handleResumeArg(sessionID string, entry *firSession, args string) {
	var sessionPath string
	if n := parseInt(args); n > 0 {
		entry.resumeMu.Lock()
		list := entry.lastResumeList
		entry.resumeMu.Unlock()
		if n <= len(list) {
			sessionPath = list[n-1].Path
		} else {
			hint := "Run /resume first to see available sessions."
			if len(list) > 0 {
				hint = fmt.Sprintf("Pick 1-%d, or run /resume to refresh the list.", len(list))
			}
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Invalid session number: %s. %s", args, hint))
			return
		}
	} else {
		sessionPath, _ = filepath.Abs(args)
	}

	sessionsDir := store.SessionsDir(entry.agentDir)
	if !IsPathWithinDirectory(sessionPath, sessionsDir) {
		pa.sendAgentMessage(sessionID, "Invalid session path: must be within sessions directory")
		return
	}

	if err := entry.session.SwitchSession(sessionPath); err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to resume session: %v", err))
	} else {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Resumed session: %s", sessionPath))
		pa.replaySessionHistory(sessionID, entry)
	}
}

// performShare creates a secret GitHub Gist from the session HTML export and
// sends back both the raw gist URL and a gistpreview.github.io preview link.
func (pa *firAgent) performShare(sessionID string, entry *firSession) {
	// Verify gh CLI is installed and authenticated.
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		if isNotFound(err) {
			pa.sendAgentMessage(sessionID, "GitHub CLI (gh) is not installed. Install it from https://cli.github.com/")
		} else {
			pa.sendAgentMessage(sessionID, "GitHub CLI is not logged in. Run 'gh auth login' first.")
		}
		return
	}

	// Export session to a temp HTML file.
	tmpPath, err := entry.session.ExportToHTML("")
	if err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to export session: %v", err))
		return
	}
	defer os.Remove(tmpPath)

	out, err := exec.Command("gh", "gist", "create", "--public=false", tmpPath).Output()
	if err != nil {
		pa.sendAgentMessage(sessionID, "Failed to create gist. Check that 'gh' is installed and authenticated.")
		return
	}

	gistURL := strings.TrimSpace(string(out))
	if gistURL == "" {
		pa.sendAgentMessage(sessionID, "Gist created but no URL returned.")
		return
	}

	// Extract the gist ID — last path component of the URL.
	// gh prints something like: https://gist.github.com/username/d168778e8e62f65886000f3f314d63e3
	gistID := gistURL[strings.LastIndex(gistURL, "/")+1:]
	if !gistIDRegex.MatchString(gistID) {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Gist created but could not parse ID from URL: %s", gistURL))
		return
	}

	previewURL := "https://gistpreview.github.io/?" + gistID
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Session shared (secret gist):\nGist: %s\nPreview: %s", gistURL, previewURL))
}

func (pa *firAgent) handleLogout(sessionID string, entry *firSession, args string) {
	authStorage := entry.modelRegistry.AuthStorage()
	creds := authStorage.GetAll()
	loggedIn := make([]string, 0, len(creds))
	for k := range creds {
		loggedIn = append(loggedIn, k)
	}
	sort.Strings(loggedIn)

	if len(loggedIn) == 0 {
		pa.sendAgentMessage(sessionID, "No providers currently logged in.")
		return
	}

	if args == "" {
		var lines []string
		for _, p := range loggedIn {
			lines = append(lines, "- "+p)
		}
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Logged in providers:\n%s\n\nTo logout: /logout <provider-id> or /logout all", strings.Join(lines, "\n")))
	} else if args == "all" {
		for _, p := range loggedIn {
			authStorage.Logout(p)
		}
		entry.modelRegistry.Refresh()
		pa.sendAgentMessage(sessionID, "Logged out from all providers.")
	} else {
		if !providerIDRegex.MatchString(args) {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Invalid provider ID: %s", args))
			return
		}
		found := false
		for _, p := range loggedIn {
			if p == args {
				found = true
				break
			}
		}
		if !found {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Provider not logged in: %s", args))
			return
		}
		authStorage.Logout(args)
		entry.modelRegistry.Refresh()
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Logged out from %s.", args))
	}
}

func (pa *firAgent) handleSkillsCommand(sessionID string, entry *firSession, args string) {
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "list" {
		pa.handleSkillsList(sessionID, entry)
		return
	}
	if parts[0] == "install" {
		if len(parts) < 2 {
			pa.sendAgentMessage(sessionID, "Usage: /skills install <name> [--user] [--force]")
			return
		}
		pa.handleSkillsInstall(sessionID, entry, parts[1:])
		return
	}
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Unknown skills subcommand: %s. Usage: /skills [list | install <name> [--user] [--force]]", parts[0]))
}

func (pa *firAgent) handleSkillsList(sessionID string, entry *firSession) {
	skills, _ := entry.session.ResourceLoader().GetSkills()
	if len(skills) == 0 {
		pa.sendAgentMessage(sessionID, "No skills loaded.")
		return
	}
	sorted := make([]resources.Skill, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Use a markdown table so ACP clients render it properly.
	var sb strings.Builder
	sb.WriteString("| Name | Source | Description |\n")
	sb.WriteString("|------|--------|-------------|\n")
	for _, s := range sorted {
		sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", s.Name, s.Source, s.Description))
	}
	pa.sendAgentMessage(sessionID, strings.TrimRight(sb.String(), "\n"))
}

func (pa *firAgent) handleSkillsInstall(sessionID string, entry *firSession, parts []string) {
	name := parts[0]
	var toUser, force bool
	for _, p := range parts[1:] {
		switch p {
		case "--user":
			toUser = true
		case "--force":
			force = true
		}
	}

	builtins := resources.LoadBuiltinSkills()
	var found bool
	for _, s := range builtins.Skills {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		available := make([]string, 0, len(builtins.Skills))
		for _, s := range builtins.Skills {
			available = append(available, s.Name)
		}
		sort.Strings(available)
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Unknown builtin skill %q. Available: %s", name, strings.Join(available, ", ")))
		return
	}

	var targetDir string
	if toUser {
		home, _ := os.UserHomeDir()
		targetDir = filepath.Join(home, ".fir", "agent", "skills", name)
	} else {
		targetDir = filepath.Join(entry.cwd, ".fir", "skills", name)
	}

	if _, err := os.Stat(targetDir); err == nil && !force {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Skill %q already exists at %s. Use --force to overwrite.", name, targetDir))
		return
	}

	prefix := "builtin_skills/" + name
	err := fs.WalkDir(resources.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(path, prefix)
		if rel == "" {
			return nil
		}
		dest := filepath.Join(targetDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := fs.ReadFile(resources.BuiltinSkillsFS, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to install skill: %v", err))
		return
	}
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Installed skill %q to %s", name, targetDir))
}

func (pa *firAgent) handleLogin(sessionID string, entry *firSession, args string) {
	authStorage := entry.modelRegistry.AuthStorage()
	providers := authStorage.GetOAuthProviders()
	if len(providers) == 0 {
		pa.sendAgentMessage(sessionID, "No OAuth providers available.")
		return
	}

	if args == "" {
		var lines []string
		for _, p := range providers {
			lines = append(lines, "- "+p.ID())
		}
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Available OAuth providers:\n%s\n\nTo login, run: /login <provider-id>", strings.Join(lines, "\n")))
		return
	}

	if !providerIDRegex.MatchString(args) {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Invalid provider ID: %s", args))
		return
	}

	var found bool
	for _, p := range providers {
		if p.ID() == args {
			found = true
			break
		}
	}
	if !found {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Provider not found: %s", args))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	err := authStorage.Login(args, oauth.LoginCallbacks{
		OnAuth: func(info oauth.AuthInfo) {
			msg := fmt.Sprintf("Open this URL to authenticate:\n%s", info.URL)
			if info.Instructions != "" {
				msg += "\n\n" + info.Instructions
			}
			pa.sendAgentMessage(sessionID, msg)
		},
		OnProgress: func(message string) {
			pa.sendAgentMessage(sessionID, message)
		},
		OnPrompt: func(prompt oauth.Prompt) (string, error) {
			// ACP mode can't do interactive prompts — return empty for optional prompts
			pa.sendAgentMessage(sessionID, prompt.Message+" (using default)")
			return "", nil
		},
		Ctx: ctx,
	})
	if err != nil {
		if ctx.Err() != nil {
			pa.sendAgentMessage(sessionID, "Login timed out after 5 minutes.")
		} else {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Login failed: %v", err))
		}
		return
	}
	entry.modelRegistry.Refresh()
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Successfully logged in to %s.", args))
}
