// ACP mode: expose firr as an ACP-compliant agent over stdio.
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/core/compaction"
	"github.com/kfet/fir/pkg/core/tools"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
)

// version is set via SetVersion before RunAcpMode.
var version = "dev"

// SetVersion sets the version string for ACP mode responses.
func SetVersion(v string) { version = v }

// firSession holds per-session state.
type firSession struct {
	session         *core.AgentSession
	modelRegistry   *core.ModelRegistry
	extensionRunner *extension.Runner
	unsubscribe     func()
	cwd             string
	agentDir        string
	termState       *terminalState
	pendingArgs     sync.Map // toolCallID → map[string]any
	resumeMu        sync.Mutex
	lastResumeList  []core.SessionListInfo
	mcpManager      *mcp.Manager // nil if no MCP servers configured
}

// firAgent implements the ACP Agent interface.
type firAgent struct {
	conn    acpConn
	options Options

	mu          sync.Mutex
	sessions    map[string]*firSession
	clientCaps  acpsdk.ClientCapabilities
	authMethods []ExtendedAuthMethod
	authStorage *core.AuthStorage // global auth storage from Initialize
}

// Compile-time interface check: piAgent must implement Agent for backward compat.
// AgentExperimental is no longer needed since we use rawMethodHandler directly.
var _ acpsdk.Agent = (*firAgent)(nil)

// builtInCommands returns the slash commands available in ACP mode.
func builtInCommands() []acpsdk.AvailableCommand {
	return []acpsdk.AvailableCommand{
		{Name: "compact", Description: "Compact the session history to save tokens"},
		{Name: "resume", Description: "List or resume a session (usage: /resume [number|path])"},
		{Name: "continue", Description: "Continue the most recent session"},
		{Name: "name", Description: "Rename the current session (usage: /name <new name>)"},
		{Name: "session", Description: "Show session statistics"},
		{Name: "changelog", Description: "Show changelog"},
		{Name: "share", Description: "Share session as a secret GitHub Gist with a preview link"},
		{Name: "export", Description: "Export session to an HTML file (usage: /export [path])"},
		{Name: "login", Description: "Login with OAuth provider (usage: /login [provider-id])"},
		{Name: "logout", Description: "Log out from provider (usage: /logout [provider-id|all])"},
		{Name: "reload", Description: "Reload extensions, skills, prompts"},
		{Name: "skills", Description: "List loaded skills (or /skills install <name>)"},
	}
}

// RunAcpMode is the entry point for ACP mode over stdin/stdout.
func RunAcpMode(opts Options) error {
	firlog.Info("acp server starting")
	pa := &firAgent{
		options:  opts,
		sessions: make(map[string]*firSession),
	}

	// Use newRawConn instead of AgentSideConnection so that session/list and
	// session/resume (not in the Go SDK's stable dispatch) are handled correctly.
	conn, done := newRawConn(pa, os.Stdout, os.Stdin)
	pa.conn = conn

	// Block until connection closes
	<-done

	// Clean up all sessions
	pa.mu.Lock()
	sessions := make(map[string]*firSession, len(pa.sessions))
	for k, v := range pa.sessions {
		sessions[k] = v
	}
	pa.mu.Unlock()

	firlog.Debug("acp shutting down", "sessions", len(sessions))
	for sid, entry := range sessions {
		CleanupPendingBashTerminals(context.Background(), conn, entry.termState, sid)
		CleanupBackgroundTerminals(context.Background(), conn, entry.termState, sid)
		if entry.unsubscribe != nil {
			entry.unsubscribe()
		}
		entry.session.Close()
		if entry.mcpManager != nil {
			_ = entry.mcpManager.Close()
		}
	}

	return nil
}

// ============================================================================
// Agent interface implementation
// ============================================================================

func (pa *firAgent) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	pa.mu.Lock()
	pa.clientCaps = params.ClientCapabilities
	pa.mu.Unlock()

	// Build auth methods from the global agent dir config.
	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		agentDir = dir
	}
	authStorage := core.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := core.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	authMethods := buildAuthMethods(authStorage, modelRegistry)

	pa.mu.Lock()
	pa.authMethods = authMethods
	pa.authStorage = authStorage
	pa.mu.Unlock()

	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentInfo:       &acpsdk.Implementation{Name: "fir", Version: version},
		AgentCapabilities: acpsdk.AgentCapabilities{
			PromptCapabilities: acpsdk.PromptCapabilities{
				Image:           true,
				EmbeddedContext: true,
			},
		},
		AuthMethods: toSDKAuthMethods(authMethods),
	}, nil
}

func (pa *firAgent) Authenticate(ctx context.Context, req acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return pa.handleAuthenticate(ctx, req)
}

func (pa *firAgent) NewSession(ctx context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	sessionID := uuid.New().String()
	firlog.Info("acp new session", "sessionID", sessionID)
	cwd := os.Getenv("PWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if params.Cwd != "" {
		cwd = params.Cwd
	}

	// Merge project-level configs with request-level configs.
	// Request-level entries take precedence over project-level ones.
	// Only stdio transport is supported; non-stdio entries are logged and skipped.
	mcpConfigs := loadProjectMCPConfigs(cwd)
	if mcpConfigs == nil && len(params.McpServers) > 0 {
		mcpConfigs = make(map[string]mcp.ServerConfig)
	}
	for _, mcpServer := range params.McpServers {
		if mcpServer.Stdio == nil {
			// Identify the server by whichever transport the SDK parsed.
			var serverName string
			switch {
			case mcpServer.Http != nil:
				serverName = mcpServer.Http.Name
			case mcpServer.Sse != nil:
				serverName = mcpServer.Sse.Name
			}
			fmt.Fprintf(os.Stderr, "fir: warning: MCP server %q uses unsupported transport (only stdio is supported); skipping\n", serverName)
			continue
		}
		envs := map[string]string{}
		for _, v := range mcpServer.Stdio.Env {
			envs[v.Name] = v.Value
		}
		mcpConfigs[mcpServer.Stdio.Name] = mcp.ServerConfig{
			Command: mcpServer.Stdio.Command,
			Args:    mcpServer.Stdio.Args,
			Env:     envs,
		}
	}

	entry, err := pa.createSession(ctx, sessionID, cwd, mcpConfigs)
	if err != nil {
		return acpsdk.NewSessionResponse{}, fmt.Errorf("create session: %w", err)
	}

	var models *acpsdk.SessionModelState
	if m := entry.session.Model(); m != nil {
		models = BuildModelState(entry.modelRegistry, m)
	}

	// Send available commands after response
	go pa.sendAvailableCommands(sessionID)

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
	var opts *core.PromptOptions
	if len(images) > 0 {
		opts = &core.PromptOptions{Images: images}
	}
	if err := entry.session.Prompt(text, opts); err != nil {
		pa.sendAgentMessage(string(params.SessionId), fmt.Sprintf("Error: %v", err))
	}

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

	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		agentDir = dir
	}
	sessionDir := core.DefaultSessionDir(agentDir, cwd)
	sessions, err := core.ListSessions(cwd, sessionDir)
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
	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		agentDir = dir
	}
	sessionsDir := core.SessionsDir(agentDir)
	sessionPath, _ := filepath.Abs(params.SessionId)
	if !IsPathWithinDirectory(sessionPath, sessionsDir) {
		return ResumeSessionResponse{}, fmt.Errorf("invalid session path: must be within sessions directory")
	}

	// Use params.SessionId as the new session's ID so the client can reference it.
	sessionID := params.SessionId

	// Close any existing session with the same ID before creating a new one.
	// Without this, a client retry would overwrite the old session's unsubscribe,
	// extensionRunner, and agent goroutine, leaking all three.
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

	// Send available commands after response.
	go func() {
		pa.sendAvailableCommands(sessionID)
	}()

	var models interface{}
	if m := entry.session.Model(); m != nil {
		models = BuildModelState(entry.modelRegistry, m)
	}
	return ResumeSessionResponse{Models: models}, nil
}

// ============================================================================
// Session creation
// ============================================================================

func (pa *firAgent) createSession(ctx context.Context, sessionID, cwd string, mcpConfigs map[string]mcp.ServerConfig) (*firSession, error) {
	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		agentDir = dir
	}

	authStorage := core.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := core.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	settingsManager := core.NewSettingsManager(cwd, agentDir)
	sessionManager := core.NewSessionManager(cwd, core.DefaultSessionDir(agentDir, cwd))

	rl := core.NewResourceLoader(core.ResourceLoaderOptions{
		Cwd:                           cwd,
		AgentDir:                      agentDir,
		SettingsManager:               settingsManager,
		NoSkills:                      pa.options.NoSkills,
		AdditionalSkillPaths:          pa.options.AdditionalSkillPaths,
		AdditionalPromptTemplatePaths: pa.options.AdditionalPromptTemplatePaths,
		NoPromptTemplates:             pa.options.NoPromptTemplates,
	})
	if err := rl.Reload(); err != nil {
		return nil, fmt.Errorf("reload resources: %w", err)
	}

	// Create tools — use ACP delegation based on client capabilities.
	pa.mu.Lock()
	caps := pa.clientCaps
	pa.mu.Unlock()
	useClientTerminal := caps.Terminal
	useClientFs := caps.Fs.WriteTextFile

	// Read settings that affect tool behavior.
	shellCommandPrefix := settingsManager.GetShellCommandPrefix()

	var toolList []agent.AgentTool
	if useClientTerminal || useClientFs {
		toolList = pa.createAcpTools(cwd, sessionID, useClientTerminal, useClientFs, shellCommandPrefix)
	} else {
		if shellCommandPrefix != "" {
			toolList = core.DefaultCodingToolsWithPrefix(cwd, shellCommandPrefix)
		} else {
			toolList = core.DefaultCodingTools(cwd)
		}
	}

	// Start MCP servers and append their tools.
	var mcpMgr *mcp.Manager
	if len(mcpConfigs) > 0 {
		mcpMgr = mcp.NewManager(mcpConfigs, false)
		mcpTools, err := mcpMgr.Start(ctx)
		if err != nil {
			return nil, fmt.Errorf("start MCP servers: %w", err)
		}
		toolList = append(toolList, mcpTools...)
	}

	result, err := core.CreateAgentSession(ctx, core.CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		SessionManager:  sessionManager,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		Tools:           toolList,
		CompactionRunner: &compaction.DefaultRunner{
			SettingsManager: settingsManager,
			ModelRegistry:   modelRegistry,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	entry := &firSession{
		session:       result.Session,
		modelRegistry: modelRegistry,
		cwd:           cwd,
		agentDir:      agentDir,
		termState:     newTerminalState(),
		mcpManager:    mcpMgr,
	}

	unsub := result.Session.Subscribe(func(event core.AgentSessionEvent) {
		pa.handleEvent(sessionID, entry, event)
	})
	entry.unsubscribe = unsub

	// Extension setup
	if !pa.options.NoExtensions {
		extSetup, err := extension.Setup(result.Session, core.NewEventBus(), extension.SetupOptions{
			EnabledNames: pa.options.EnabledExtensions,
			ProjectDir:   cwd,
			Cwd:          cwd,
		})
		if err == nil && extSetup != nil && extSetup.Runner != nil {
			entry.extensionRunner = extSetup.Runner
			_ = extSetup.Runner.EmitSessionStart()
		}
	}

	pa.mu.Lock()
	pa.sessions[sessionID] = entry
	pa.mu.Unlock()

	return entry, nil
}

// loadProjectMCPConfigs reads `.fir/mcp.json` from the working directory and
// returns any MCP server configurations found. Returns nil if the file does
// not exist. Logs a warning to stderr if the file exists but cannot be read
// or parsed, so the user knows why no MCP servers were started.
func loadProjectMCPConfigs(cwd string) map[string]mcp.ServerConfig {
	cfg, err := mcp.LoadConfigFile(filepath.Join(cwd, ".fir", "mcp.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fir: warning: %v — no MCP servers will be started\n", err)
		return nil
	}
	if cfg == nil {
		return nil
	}
	return cfg.MCPServers
}

// createAcpTools creates tools with ACP delegation based on client capabilities.
// useClientTerminal: route bash execution through ACP client terminal.
// useClientFs: route read/write/edit file I/O through ACP client fs methods.
// shellCommandPrefix: optional prefix prepended to every bash command.
func (pa *firAgent) createAcpTools(cwd, sessionID string, useClientTerminal, useClientFs bool, shellCommandPrefix string) []agent.AgentTool {
	var readTool agent.AgentTool
	var editTool agent.AgentTool
	var writeTool agent.AgentTool
	if useClientFs {
		readTool = pa.createAcpReadTool(cwd, sessionID)
		editTool = pa.createAcpEditTool(cwd, sessionID)
		writeTool = pa.createAcpWriteTool(cwd, sessionID)
	} else {
		readTool = tools.NewReadTool(cwd)
		editTool = tools.NewEditTool(cwd)
		writeTool = tools.NewWriteTool(cwd)
	}

	var bashTool agent.AgentTool
	if useClientTerminal {
		bashTool = pa.createAcpBashTool(cwd, sessionID, shellCommandPrefix)
	} else {
		bashTool = tools.NewBashToolWithPrefix(cwd, shellCommandPrefix)
	}

	toolList := []agent.AgentTool{
		readTool,
		bashTool,
		editTool,
		writeTool,
		tools.NewGrepTool(cwd),
		tools.NewFindTool(cwd),
		tools.NewLsTool(cwd),
	}
	if useClientTerminal {
		toolList = append(toolList,
			pa.createBashOutputTool(sessionID),
			pa.createBashKillTool(sessionID),
		)
	}
	return toolList
}

// createAcpReadFn returns a ReadFileFn that delegates to the ACP client.
func (pa *firAgent) createAcpReadFn(sessionID string) tools.ReadFileFn {
	return func(ctx context.Context, path string) (string, error) {
		resp, err := pa.conn.ReadTextFile(ctx, acpsdk.ReadTextFileRequest{
			SessionId: acpsdk.SessionId(sessionID),
			Path:      path,
		})
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}

// createAcpWriteFn returns a WriteFileFn that delegates to the ACP client.
func (pa *firAgent) createAcpWriteFn(sessionID string) tools.WriteFileFn {
	return func(ctx context.Context, path, content string) error {
		_, err := pa.conn.WriteTextFile(ctx, acpsdk.WriteTextFileRequest{
			SessionId: acpsdk.SessionId(sessionID),
			Path:      path,
			Content:   content,
		})
		return err
	}
}

// createAcpReadTool creates a read tool delegating to the ACP client.
func (pa *firAgent) createAcpReadTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewReadToolWithReader(cwd, pa.createAcpReadFn(sessionID))
}

// createAcpWriteTool creates a write tool delegating to the ACP client.
func (pa *firAgent) createAcpWriteTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewWriteToolWithWriter(cwd, pa.createAcpWriteFn(sessionID))
}

// createAcpEditTool creates an edit tool delegating file I/O to the ACP client.
func (pa *firAgent) createAcpEditTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewEditToolWithReadWriter(cwd,
		pa.createAcpReadFn(sessionID),
		pa.createAcpWriteFn(sessionID),
	)
}

func (pa *firAgent) createAcpBashTool(cwd, sessionID, shellCommandPrefix string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command. Output truncated to %d lines or %dKB. Set run_in_background for long-running processes.",
				tools.DefaultMaxLines, tools.DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":           map[string]any{"type": "string", "description": "Bash command to execute"},
					"timeout":           map[string]any{"type": "number", "description": "Timeout in seconds (optional)"},
					"run_in_background": map[string]any{"type": "boolean", "description": "Run in background, returns command_id"},
				},
				"required": []string{"command"},
			},
		},
		Label: "bash",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			command, _ := params["command"].(string)
			timeout, _ := params["timeout"].(float64)
			runInBg, _ := params["run_in_background"].(bool)

			// Apply shell command prefix if configured.
			if shellCommandPrefix != "" {
				command = shellCommandPrefix + "\n" + command
			}

			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			if runInBg {
				commandID, err := StartBackgroundCommand(ctx, pa.conn, entry.termState, sessionID, command, cwd, toolCallID)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: fmt.Sprintf("Background command started with ID: %s\nUse bash_output to check output and bash_kill to terminate.", commandID)}},
				}, nil
			}

			// Foreground: use ACP terminal
			result, err := AcpBashExec(ctx, pa.conn, entry.termState, sessionID, toolCallID, command, cwd, int(timeout))
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			text := result.Output
			if text == "" {
				text = "(no output)"
			}
			if result.ExitCode != nil && *result.ExitCode != 0 {
				text += fmt.Sprintf("\n\nCommand exited with code %d", *result.ExitCode)
				return agent.AgentToolResult{}, errors.New(text)
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

func (pa *firAgent) createBashOutputTool(sessionID string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "bash_output",
			Description: "Get the output of a background bash command.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command_id": map[string]any{"type": "string", "description": "ID of the background command"},
				},
				"required": []string{"command_id"},
			},
		},
		Label: "bash output",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			commandID, _ := params["command_id"].(string)
			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			output, isRunning, exitCode, err := GetBackgroundOutput(ctx, pa.conn, entry.termState, sessionID, commandID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			status := "running"
			if !isRunning {
				ec := "nil"
				if exitCode != nil {
					ec = fmt.Sprintf("%d", *exitCode)
				}
				status = fmt.Sprintf("exited (code %s)", ec)
			}
			if output == "" {
				output = "(no output)"
			}
			text := fmt.Sprintf("Status: %s\n\n%s", status, output)

			if !isRunning && exitCode != nil && *exitCode != 0 {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: text}},
				}, nil
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

func (pa *firAgent) createBashKillTool(sessionID string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "bash_kill",
			Description: "Kill a background bash command and return its final output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command_id": map[string]any{"type": "string", "description": "ID of the background command to kill"},
				},
				"required": []string{"command_id"},
			},
		},
		Label: "bash kill",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			commandID, _ := params["command_id"].(string)
			pa.mu.Lock()
			entry, ok := pa.sessions[sessionID]
			pa.mu.Unlock()
			if !ok {
				return agent.AgentToolResult{}, fmt.Errorf("session not found")
			}

			output, exitCode, err := KillBackgroundCommand(ctx, pa.conn, entry.termState, sessionID, commandID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if output == "" {
				output = "(no output)"
			}
			ec := "nil"
			if exitCode != nil {
				ec = fmt.Sprintf("%d", *exitCode)
			}
			text := fmt.Sprintf("Command killed (exit code %s)\n\n%s", ec, output)
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

// ============================================================================
// Event handling
// ============================================================================

func (pa *firAgent) handleEvent(sessionID string, entry *firSession, event core.AgentSessionEvent) {
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
				// Notify extensions that work is resuming
				if entry.extensionRunner != nil {
					_ = entry.extensionRunner.EmitAgentStart()
				}
				go func() { _ = entry.session.Agent.Continue() }()
				pa.sendAgentMessage(sessionID, "Session compacted successfully. Resuming.")
			} else {
				pa.sendAgentMessage(sessionID, "Session compacted successfully.")
			}
		}
		return true

	case "resume":
		if args == "" {
			sessionDir := core.DefaultSessionDir(entry.agentDir, entry.cwd)
			sessions, _ := core.ListSessions(entry.cwd, sessionDir)
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
		sessionDir := core.DefaultSessionDir(entry.agentDir, entry.cwd)
		sessions, _ := core.ListSessions(entry.cwd, sessionDir)
		if len(sessions) == 0 {
			pa.sendAgentMessage(sessionID, "No sessions available to continue.")
			return true
		}
		sessionsDir := core.SessionsDir(entry.agentDir)
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
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Continued session: %s\nNote: Previous message history is not visible in this client view.", name))
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
		if name != "" {
			info += fmt.Sprintf("**Name:** %s\n", name)
		}
		info += fmt.Sprintf("**ID:** %s\n\n", stats.SessionID)
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
		entries := core.GetChangelogEntries()
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
			pa.sendAvailableCommands(sessionID)
			pa.sendAgentMessage(sessionID, "Reload completed successfully.")
		}
		return true

	case "skills":
		pa.handleSkillsCommand(sessionID, entry, args)
		return true

	default:
		// Check extension commands first (matches interactive/mode.go ordering)
		if entry.extensionRunner != nil {
			if found, err := entry.extensionRunner.ExecuteCommand(command, args); found {
				if err != nil {
					pa.sendAgentMessage(sessionID, fmt.Sprintf("Extension command error: %v", err))
				}
				return true
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
				expanded := core.ExpandPromptTemplate(fullCmd, templates)
				if expanded != fullCmd {
					_ = entry.session.Prompt(expanded)
				}
				return true
			}
		}

		// Check skill commands
		if strings.HasPrefix(command, "skill:") {
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

	sessionsDir := core.SessionsDir(entry.agentDir)
	if !IsPathWithinDirectory(sessionPath, sessionsDir) {
		pa.sendAgentMessage(sessionID, "Invalid session path: must be within sessions directory")
		return
	}

	if err := entry.session.SwitchSession(sessionPath); err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to resume session: %v", err))
	} else {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Resumed session: %s\nNote: Previous message history is not visible in this client view.", sessionPath))
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

// gistIDRegex matches a valid GitHub Gist ID (hex string of at least 20 characters).
var gistIDRegex = regexp.MustCompile(`^[a-fA-F0-9]{20,}$`)

// isNotFound reports whether an exec error is "command not found".
func isNotFound(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	return true // LookupError / exec.ErrNotFound etc.
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

// providerIDRegex validates provider IDs (alphanumeric with hyphens).
var providerIDRegex = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

func (pa *firAgent) handleSkillsCommand(sessionID string, entry *firSession, args string) {
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "list" {
		skills, _ := entry.session.ResourceLoader().GetSkills()
		if len(skills) == 0 {
			pa.sendAgentMessage(sessionID, "No skills loaded.")
			return
		}
		sorted := make([]core.Skill, len(skills))
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
		return
	}
	if parts[0] == "install" {
		if len(parts) < 2 {
			pa.sendAgentMessage(sessionID, "Usage: /skills install <name> [--user] [--force]")
			return
		}
		name := parts[1]
		var toUser, force bool
		for _, p := range parts[2:] {
			switch p {
			case "--user":
				toUser = true
			case "--force":
				force = true
			}
		}

		builtins := core.LoadBuiltinSkills()
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
		err := fs.WalkDir(core.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, walkErr error) error {
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
			data, err := fs.ReadFile(core.BuiltinSkillsFS, path)
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
		return
	}
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Unknown skills subcommand: %s. Usage: /skills [list | install <name> [--user] [--force]]", parts[0]))
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

// ============================================================================
// Helpers
// ============================================================================

func (pa *firAgent) sendAgentMessage(sessionID, text string) {
	_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update:    acpsdk.UpdateAgentMessageText(text),
	})
}

func (pa *firAgent) sendAvailableCommands(sessionID string) {
	pa.mu.Lock()
	entry, ok := pa.sessions[sessionID]
	pa.mu.Unlock()
	if !ok {
		return
	}

	commands := builtInCommands()

	// Add prompt template commands
	templates, _ := entry.session.ResourceLoader().GetPrompts()
	for _, t := range templates {
		commands = append(commands, acpsdk.AvailableCommand{Name: t.Name, Description: t.Description})
	}

	// Add skill commands
	skills, _ := entry.session.ResourceLoader().GetSkills()
	for _, s := range skills {
		desc := s.Description
		if desc == "" {
			desc = "Skill: " + s.Name
		}
		commands = append(commands, acpsdk.AvailableCommand{Name: "skill:" + s.Name, Description: desc})
	}

	// Add extension commands
	if entry.extensionRunner != nil {
		for name, cmd := range entry.extensionRunner.GetCommands() {
			desc := cmd.Description
			if desc == "" {
				desc = "(extension command)"
			}
			commands = append(commands, acpsdk.AvailableCommand{Name: name, Description: desc})
		}
	}

	_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update: acpsdk.SessionUpdate{
			AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
				AvailableCommands: commands,
			},
		},
	})
}

// parseInt parses a non-negative integer from s (trimming whitespace).
// Returns 0 for empty, non-numeric, or negative input — callers use n > 0 as a validity check.
func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
