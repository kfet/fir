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
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/extension"
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
	// Eagerly start auth-provider extensions so OAuth providers (like
	// anthropic, codex, copilot, …) are registered in the global oauth
	// registry before we enumerate AuthMethods. Without this the ACP
	// client would only see env-var methods.
	if !pa.options.NoExtensions {
		cwd, _ := os.Getwd()
		authSetup, aerr := extension.SetupAuthProviders(extension.AuthSetupOptions{
			ProjectDir:    cwd,
			Cwd:           cwd,
			Mode:          "acp",
			Version:       version,
			EnabledNames:  pa.options.EnabledExtensions,
			DisabledNames: pa.options.DisabledExtensions,
		})
		if aerr != nil {
			firlog.Warn("acp initialize: auth extension setup failed", "err", aerr)
		}
		pa.mu.Lock()
		oldAuthExt := pa.authExtSetup
		pa.authExtSetup = authSetup
		pa.mu.Unlock()
		if oldAuthExt != nil {
			oldAuthExt.Stop()
		}
		firlog.Info("acp initialize: auth extensions started", "elapsed_ms", time.Since(t0).Milliseconds())
	}

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
	mcpConfigs = mergeRequestMCPServers(mcpConfigs, params.McpServers)

	t0 = time.Now()
	entry, err := pa.createSession(ctx, sessionID, cwd, mcpConfigs)
	firlog.Info("acp new session: createSession done", "elapsed_ms", time.Since(t0).Milliseconds())
	if err != nil {
		return acpsdk.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}
	// Store client-provided MCP configs so /reload can re-merge them.
	entry.clientMCPConfigs = mergeRequestMCPServers(nil, params.McpServers)

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
		// The session isn't in memory — it was likely reaped for idleness (an
		// idle session holds ZERO sidecars), or it exists only on disk. Try to
		// re-hydrate it in place under the SAME sessionID so waking an idle
		// conversation is seamless: no ID churn, no session-not-found
		// round-trip. Only surface session-not-found when there is genuinely
		// nothing to resume.
		rehydrated, err := pa.rehydrateForPrompt(ctx, string(params.SessionId))
		if err != nil {
			return acpsdk.PromptResponse{}, err
		}
		if rehydrated == nil {
			return acpsdk.PromptResponse{}, newSessionNotFound(string(params.SessionId))
		}
		entry = rehydrated
	}
	entry.touch(pa.now())

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

	// Run any self-handoff requested during the turn inline, so the fresh
	// briefed turn's output streams within this same prompt response. The
	// relay treats the prompt as complete once we return StopReason, so a
	// handoff turn started on a detached goroutine (the interactive
	// approach) would be lost.
	pa.runPendingHandoffs(string(params.SessionId), entry)

	// Clear plan at end of turn so the next turn starts fresh.
	entry.plan.clear()

	return acpsdk.PromptResponse{StopReason: acpsdk.StopReasonEndTurn}, nil
}

// maxChainedHandoffs bounds how many self_handoff hops a single ACP prompt
// follows inline, so a briefing that immediately re-hands-off cannot loop
// forever within one turn.
const maxChainedHandoffs = 8

// runPendingHandoffs consumes any self_handoff restart requested during the
// just-finished turn and runs the fresh, briefed turn inline. The self_handoff
// tool aborts the current turn and records the request synchronously on the
// bridge (TakePendingRestart); here — after the aborted turn has unwound — we
// reset the session, inject the briefing, and submit the continuation prompt,
// streaming its output over the same ACP session within the current prompt
// response. Chained handoffs are followed up to maxChainedHandoffs times.
//
// Relies on ACP being per-session serial: the relay never issues two
// concurrent Prompt() calls for one session, so the pendingRestart recorded
// during this turn is always consumed by this turn and never by a sibling.
func (pa *firAgent) runPendingHandoffs(sessionID string, entry *firSession) {
	if entry == nil || entry.extSetup == nil || entry.extSetup.Bridge == nil {
		return
	}
	for i := 0; i < maxChainedHandoffs; i++ {
		// The aborted turn must be fully unwound before we start a new one.
		entry.session.Agent.WaitForIdle()
		prompt, prepend, ok := entry.extSetup.Bridge.TakePendingRestart()
		if !ok {
			return
		}
		if _, err := entry.session.NewSessionCmd(); err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Handoff failed: could not start a new session: %v", err))
			return
		}
		if prepend != "" {
			entry.session.PrependContext(prepend)
		}
		if prompt != "" {
			if err := entry.session.Prompt(prompt); err != nil {
				pa.sendAgentMessage(sessionID, fmt.Sprintf("Handoff: continuation prompt failed: %v", err))
				return
			}
		}
	}
	// Hit the chain cap — surface it rather than silently swallow further
	// pending handoffs.
	pa.sendAgentMessage(sessionID, "Handoff: stopped after too many chained handoffs in one turn.")
}

func (pa *firAgent) Cancel(_ context.Context, params acpsdk.CancelNotification) error {
	pa.mu.Lock()
	entry, ok := pa.sessions[string(params.SessionId)]
	pa.mu.Unlock()
	if !ok {
		return newSessionNotFound(string(params.SessionId))
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
		return acpsdk.SetSessionModelResponse{}, newSessionNotFound(string(params.SessionId))
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
		legacyDirs = []string{session.PiAgentDir()}
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

	// Validate session path is within any known sessions directory to prevent traversal.
	// If the sessionId is a bare UUID (from session/new), resolve it to the actual
	// session file path by searching known session directories.
	agentDir := resolveAgentDir()
	sessionPath := params.SessionId
	createFreshInstead := false

	// Check if sessionId looks like a UUID (not a file path).
	if !strings.Contains(params.SessionId, string(filepath.Separator)) && !strings.Contains(params.SessionId, ".jsonl") {
		// Try to find the session file by UUID in known session directories.
		resolved := resolveSessionByUUID(params.SessionId, agentDir, cwd)
		if resolved == "" {
			// No matching session file — create a fresh session instead.
			firlog.Info("session/resume: UUID not found, creating fresh session",
				"sessionId", params.SessionId, "cwd", cwd)
			createFreshInstead = true
		} else {
			sessionPath = resolved
		}
	}

	if createFreshInstead {
		var mcpConfigs map[string]mcp.ServerConfig
		if !pa.options.NoMCP {
			mcpConfigs = loadProjectMCPConfigs(cwd, pa.options.MCPConfig)
		}
		mcpConfigs = mergeRequestMCPServers(mcpConfigs, params.McpServers)
		entry, err := pa.createSession(ctx, params.SessionId, cwd, mcpConfigs)
		if err != nil {
			return ResumeSessionResponse{}, fmt.Errorf("create session: %w", err)
		}
		entry.clientMCPConfigs = mergeRequestMCPServers(nil, params.McpServers)
		var mdls interface{}
		if m := entry.session.Model(); m != nil {
			mdls = BuildModelState(entry.modelRegistry, m)
		}
		return ResumeSessionResponse{Models: mdls}, nil
	}

	absPath, err := filepath.Abs(sessionPath)
	if err != nil {
		return ResumeSessionResponse{}, fmt.Errorf("invalid session path %q: %w", params.SessionId, err)
	}
	sessionPath = absPath

	if !isValidSessionPath(sessionPath, agentDir) {
		firlog.Warn("session/resume: path validation failed",
			"sessionId", params.SessionId,
			"resolvedPath", sessionPath,
			"agentDir", agentDir,
			"sessionsDir", store.SessionsDir(agentDir),
		)
		return ResumeSessionResponse{}, fmt.Errorf("invalid session path: must be within sessions directory (sessionId=%q, resolved=%q, sessionsDir=%q)", params.SessionId, sessionPath, store.SessionsDir(agentDir))
	}

	// Use params.SessionId as the new session's ID so the client can reference it.
	sessionID := params.SessionId

	entry, forked, err := pa.hydrateSessionFromFile(ctx, sessionID, sessionPath, cwd, params.McpServers)
	if err != nil {
		return ResumeSessionResponse{}, err
	}
	if forked {
		pa.sendAgentMessage(sessionID, "Session is active in another window — branched with history preserved.")
	}

	var models interface{}
	if m := entry.session.Model(); m != nil {
		models = BuildModelState(entry.modelRegistry, m)
	}
	return ResumeSessionResponse{Models: models}, nil
}

// hydrateSessionFromFile creates an in-memory session entry for sessionID and
// switches it to the on-disk transcript at sessionPath. Any existing in-memory
// session with the same ID is torn down first. It is the shared setup path used
// by both session/resume and Prompt-driven lazy re-hydration. Returns the new
// entry and whether the session was forked (active in another window).
//
// Heavy setup (createSession, extension start, SwitchSession) runs outside
// pa.mu; createSession registers the entry in pa.sessions under the lock.
func (pa *firAgent) hydrateSessionFromFile(ctx context.Context, sessionID, sessionPath, cwd string, mcpServers []acpsdk.McpServer) (*firSession, bool, error) {
	// Close any existing session with the same ID before creating a new one.
	// Without this, a client retry would overwrite the old session's
	// unsubscribe, extSetup, and agent goroutine, leaking all three.
	if existing, ok := pa.removeSession(sessionID); ok {
		pa.teardownSession(ctx, sessionID, existing)
	}

	var mcpConfigs map[string]mcp.ServerConfig
	if !pa.options.NoMCP {
		mcpConfigs = loadProjectMCPConfigs(cwd, pa.options.MCPConfig)
	}
	mcpConfigs = mergeRequestMCPServers(mcpConfigs, mcpServers)

	entry, err := pa.createSession(ctx, sessionID, cwd, mcpConfigs)
	if err != nil {
		return nil, false, fmt.Errorf("create session: %w", err)
	}
	entry.clientMCPConfigs = mergeRequestMCPServers(nil, mcpServers)

	// Wait for async extension setup so the EmitSessionStart goroutine
	// (which reads session state via GetSessionName) finishes before we
	// mutate the session via SwitchSession. Without this the two race on
	// SessionStore internals.
	if entry.extReady != nil {
		<-entry.extReady
	}

	// Switch to the requested session file.
	forked, err := entry.session.SwitchSession(sessionPath)
	if err != nil {
		return nil, false, fmt.Errorf("switch session: %w", err)
	}
	return entry, forked, nil
}

// rehydrateForPrompt brings sessionID back into memory after it was reaped for
// idleness (or when it lives only on disk), re-hydrating it under the SAME
// sessionID. Returns the entry on success, (nil, nil) when there is no session
// to resume (caller surfaces session-not-found), or (nil, err) when
// re-hydration itself failed.
func (pa *firAgent) rehydrateForPrompt(ctx context.Context, sessionID string) (*firSession, error) {
	// Prefer the reaper's recorded transcript+cwd. The ACP sessionID has no
	// on-disk link (the store names files by its own UUID), so this in-process
	// map is the only reliable sessionID→file mapping.
	if r, ok := pa.takeReaped(sessionID); ok {
		cwd := r.cwd
		if cwd == "" {
			cwd = defaultPromptCwd()
		}
		var entry *firSession
		var err error
		if !fileExists(r.file) {
			// Reaped session with no persisted transcript (empty path, or the
			// file vanished): re-create it fresh under the same ID so the
			// conversation continues seamlessly.
			entry, err = pa.createSession(ctx, sessionID, cwd, pa.sessionMCPConfigs(cwd))
		} else {
			entry, _, err = pa.hydrateSessionFromFile(ctx, sessionID, r.file, cwd, nil)
		}
		if err != nil {
			// Re-insert the record so a retry can still recover; a transient
			// failure must not permanently lose the only sessionID→file
			// mapping (which would force ID churn on the next prompt).
			pa.restoreReaped(sessionID, r)
			firlog.Warn("acp prompt: re-hydration failed", "sessionId", sessionID, "err", err)
			return nil, err
		}
		firlog.Info("acp prompt: re-hydrated reaped session in place", "sessionId", sessionID)
		return entry, nil
	}

	// No reaped record. A concurrent Prompt may have just re-hydrated this same
	// id and consumed the record — re-check the live map before giving up so we
	// don't spuriously surface session-not-found.
	if entry := pa.lookupSession(sessionID); entry != nil {
		return entry, nil
	}

	// See if sessionID resolves to an on-disk session file (an explicit path or
	// store UUID the client retained). cwd is unknown here — fall back to the
	// process cwd; the main reaped path above uses the recorded session cwd.
	cwd := defaultPromptCwd()
	sessionPath := pa.resolveSessionFilePath(sessionID, cwd)
	if sessionPath == "" {
		// Final re-check: another goroutine may have registered it meanwhile.
		if entry := pa.lookupSession(sessionID); entry != nil {
			return entry, nil
		}
		return nil, nil // genuinely unknown — caller surfaces session-not-found
	}
	entry, _, err := pa.hydrateSessionFromFile(ctx, sessionID, sessionPath, cwd, nil)
	if err != nil {
		firlog.Warn("acp prompt: on-disk re-hydration failed", "sessionId", sessionID, "err", err)
		return nil, err
	}
	firlog.Info("acp prompt: hydrated on-disk session in place", "sessionId", sessionID)
	return entry, nil
}

// lookupSession returns the in-memory session for sessionID, or nil.
func (pa *firAgent) lookupSession(sessionID string) *firSession {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return pa.sessions[sessionID]
}

// sessionMCPConfigs loads the MCP server configs for a session at cwd, honouring
// the --no-mcp option.
func (pa *firAgent) sessionMCPConfigs(cwd string) map[string]mcp.ServerConfig {
	if pa.options.NoMCP {
		return nil
	}
	return loadProjectMCPConfigs(cwd, pa.options.MCPConfig)
}

// resolveSessionFilePath resolves a sessionID to an existing on-disk session
// file path, or "" if none exists. It accepts either a bare UUID (resolved by
// searching known session dirs) or an explicit path (validated to live within
// a sessions directory).
func (pa *firAgent) resolveSessionFilePath(sessionID, cwd string) string {
	agentDir := resolveAgentDir()
	if !strings.Contains(sessionID, string(filepath.Separator)) && !strings.Contains(sessionID, ".jsonl") {
		return resolveSessionByUUID(sessionID, agentDir, cwd)
	}
	absPath, err := filepath.Abs(sessionID)
	if err != nil {
		return ""
	}
	if !isValidSessionPath(absPath, agentDir) || !fileExists(absPath) {
		return ""
	}
	return absPath
}

// defaultPromptCwd returns the working directory to use when re-hydrating a
// session whose original cwd is unknown.
func defaultPromptCwd() string {
	if cwd := os.Getenv("PWD"); cwd != "" {
		return cwd
	}
	cwd, _ := os.Getwd()
	return cwd
}

// fileExists reports whether path names an existing file.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// replaySessionHistory pushes the historical messages from a resumed session
// to the ACP client as session update notifications. This allows the client
// to display the full conversation history from previous turns.
func (pa *firAgent) replaySessionHistory(sessionID string, entry *firSession) {
	ctx := entry.session.SessionStore.BuildSessionContext()
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
				_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.UpdateUserMessageText(text)))
			}

		case "assistant":
			am := msg.AsAssistant()
			if am == nil {
				continue
			}
			for _, c := range am.Content {
				if c.IsText() && c.Text != nil {
					_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.UpdateAgentMessageText(c.Text.Text)))
				} else if c.IsServerContent() && c.Server != nil && c.Server.Display != "" {
					_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.UpdateAgentMessageText(c.Server.Display)))
				} else if c.IsThinking() && c.Thinking != nil {
					_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.UpdateAgentThoughtText(c.Thinking.Thinking)))
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
					_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.StartToolCall(acpsdk.ToolCallId(tc.ID), BuildToolTitle(tc.Name, tc.Arguments), startOpts...)))
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

			_ = pa.conn.SessionUpdate(context.Background(), entry.notification(sid, acpsdk.UpdateToolCall(acpsdk.ToolCallId(tr.ToolCallID), updateOpts...)))
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
	// Mark the session active on every event so a session that is actively
	// streaming a long turn (with no new prompt) is never seen as idle by the
	// reaper and torn down mid-stream.
	entry.touch(pa.now())

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
			_ = pa.conn.SessionUpdate(context.Background(), entry.notification(acpsdk.SessionId(sessionID), acpsdk.UpdateAgentMessageText(msg.Delta)))
		case "thinking_delta":
			_ = pa.conn.SessionUpdate(context.Background(), entry.notification(acpsdk.SessionId(sessionID), acpsdk.UpdateAgentThoughtText(msg.Delta)))
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

		_ = pa.conn.SessionUpdate(context.Background(), entry.notification(acpsdk.SessionId(sessionID), acpsdk.StartToolCall(acpsdk.ToolCallId(ev.ToolCallID), BuildToolTitle(ev.ToolName, argsMap), startOpts...)))

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

		_ = pa.conn.SessionUpdate(context.Background(), entry.notification(acpsdk.SessionId(sessionID), acpsdk.UpdateToolCall(acpsdk.ToolCallId(ev.ToolCallID), updateOpts...)))

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
		// Suppress cancellation noise rather than surfacing it as an error.
		// A user cancel reports StopReasonAborted; an abort that lands while an
		// inference HTTP request is in flight (e.g. self_handoff aborting the
		// turn to restart) instead surfaces the request's context-cancellation
		// as StopReasonError with a "context canceled" message. Neither is a
		// genuine model/API failure, so neither should reach the client.
		if msg.StopReason == ai.StopReasonAborted || strings.Contains(errText, "context canceled") {
			return
		}
		pa.sendAgentMessage(sessionID, fmt.Sprintf("⚠️ %s", errText))
	}
}

// isValidSessionPath checks whether sessionPath is within any known sessions
// directory (primary + legacy). This prevents path-traversal while still
// allowing resume of sessions created under legacy agent directories.
func isValidSessionPath(sessionPath, agentDir string) bool {
	dirs := []string{agentDir}
	if os.Getenv("FIR_AGENT_DIR") == "" {
		dirs = append(dirs, session.PiAgentDir())
	}
	for _, d := range dirs {
		if IsPathWithinDirectory(sessionPath, store.SessionsDir(d)) {
			return true
		}
	}
	return false
}

// resolveSessionByUUID searches known session directories for a session file
// whose name contains the given UUID. Returns the full path if found, or "".
func resolveSessionByUUID(uuid, agentDir, cwd string) string {
	// Build list of directories to search: cwd-specific dir first, then all dirs.
	dirs := []string{agentDir}
	if os.Getenv("FIR_AGENT_DIR") == "" {
		dirs = append(dirs, session.PiAgentDir())
	}

	// First check the cwd-specific session directory for each agent dir.
	if cwd != "" {
		for _, d := range dirs {
			sessionDir := store.DefaultSessionDir(d, cwd)
			if path := findSessionFileByUUID(sessionDir, uuid); path != "" {
				return path
			}
		}
	}

	// Fall back to scanning all session subdirectories.
	for _, d := range dirs {
		sessionsRoot := store.SessionsDir(d)
		entries, err := os.ReadDir(sessionsRoot)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(sessionsRoot, entry.Name())
			if path := findSessionFileByUUID(dir, uuid); path != "" {
				return path
			}
		}
	}
	return ""
}

// findSessionFileByUUID looks for a .jsonl file containing the UUID in the given directory.
func findSessionFileByUUID(dir, uuid string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), uuid) && strings.HasSuffix(e.Name(), ".jsonl") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}
