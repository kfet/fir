// ACP mode: expose firr as an ACP-compliant agent over stdio.
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
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
)

// version is set via SetVersion before RunAcpMode.
var version = "dev"

// SetVersion sets the version string for ACP mode responses.
func SetVersion(v string) { version = v }

// piSession holds per-session state.
type piSession struct {
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
}

// piAgent implements the ACP Agent interface.
type piAgent struct {
	conn    acpConn
	options Options

	mu         sync.Mutex
	sessions   map[string]*piSession
	clientCaps acpsdk.ClientCapabilities
}

// Compile-time interface check: piAgent must implement Agent for backward compat.
// AgentExperimental is no longer needed since we use rawMethodHandler directly.
var _ acpsdk.Agent = (*piAgent)(nil)

// builtInCommands returns the slash commands available in ACP mode.
func builtInCommands() []acpsdk.AvailableCommand {
	return []acpsdk.AvailableCommand{
		{Name: "compact", Description: "Compact the session history to save tokens"},
		{Name: "resume", Description: "List or resume a session (usage: /resume [number|path])"},
		{Name: "continue", Description: "Continue the most recent session"},
		{Name: "name", Description: "Rename the current session (usage: /name <new name>)"},
		{Name: "session", Description: "Show session statistics"},
		{Name: "changelog", Description: "Show changelog"},
		{Name: "login", Description: "Login with OAuth provider (usage: /login [provider-id])"},
		{Name: "logout", Description: "Log out from provider (usage: /logout [provider-id|all])"},
		{Name: "reload", Description: "Reload extensions, skills, prompts"},
	}
}

// RunAcpMode is the entry point for ACP mode over stdin/stdout.
func RunAcpMode(opts Options) error {
	pa := &piAgent{
		options:  opts,
		sessions: make(map[string]*piSession),
	}

	// Use newRawConn instead of AgentSideConnection so that session/list and
	// session/resume (not in the Go SDK's stable dispatch) are handled correctly.
	conn, done := newRawConn(pa, os.Stdout, os.Stdin)
	pa.conn = conn

	// Block until connection closes
	<-done

	// Clean up all sessions
	pa.mu.Lock()
	sessions := make(map[string]*piSession, len(pa.sessions))
	for k, v := range pa.sessions {
		sessions[k] = v
	}
	pa.mu.Unlock()

	for sid, entry := range sessions {
		CleanupPendingBashTerminals(context.Background(), conn, entry.termState, sid)
		CleanupBackgroundTerminals(context.Background(), conn, entry.termState, sid)
		if entry.unsubscribe != nil {
			entry.unsubscribe()
		}
		entry.session.Close()
	}

	return nil
}

// ============================================================================
// Agent interface implementation
// ============================================================================

func (pa *piAgent) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	pa.mu.Lock()
	pa.clientCaps = params.ClientCapabilities
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
	}, nil
}

func (pa *piAgent) Authenticate(_ context.Context, _ acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, nil
}

func (pa *piAgent) NewSession(ctx context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	sessionID := uuid.New().String()
	cwd := os.Getenv("PWD")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if params.Cwd != "" {
		cwd = params.Cwd
	}

	entry, err := pa.createSession(ctx, sessionID, cwd)
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

func (pa *piAgent) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
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

func (pa *piAgent) Cancel(_ context.Context, params acpsdk.CancelNotification) error {
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

func (pa *piAgent) SetSessionMode(_ context.Context, _ acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, nil
}

// ============================================================================
// ============================================================================
// Unstable ACP methods (session/set_model, session/list, session/resume)
// These are handled by rawMethodHandler in conn.go, which calls these methods.
// ============================================================================

func (pa *piAgent) SetSessionModel(_ context.Context, params acpsdk.SetSessionModelRequest) (acpsdk.SetSessionModelResponse, error) {
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
func (pa *piAgent) ListSessions(_ context.Context, params ListSessionsRequest) (ListSessionsResponse, error) {
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
func (pa *piAgent) ResumeSession(ctx context.Context, params ResumeSessionRequest) (ResumeSessionResponse, error) {
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

	entry, err := pa.createSession(ctx, sessionID, cwd)
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

func (pa *piAgent) createSession(ctx context.Context, sessionID, cwd string) (*piSession, error) {
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

	entry := &piSession{
		session:       result.Session,
		modelRegistry: modelRegistry,
		cwd:           cwd,
		agentDir:      agentDir,
		termState:     newTerminalState(),
	}

	unsub := result.Session.Subscribe(func(event core.AgentSessionEvent) {
		pa.handleEvent(sessionID, entry, event)
	})
	entry.unsubscribe = unsub

	// Extension setup
	if !pa.options.NoExtensions {
		extSetup, err := extension.Setup(result.Session, core.NewEventBus(), extension.SetupOptions{
			EnabledNames: pa.options.EnabledExtensions,
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

// createAcpTools creates tools with ACP delegation based on client capabilities.
// useClientTerminal: route bash execution through ACP client terminal.
// useClientFs: route read/write/edit file I/O through ACP client fs methods.
// shellCommandPrefix: optional prefix prepended to every bash command.
func (pa *piAgent) createAcpTools(cwd, sessionID string, useClientTerminal, useClientFs bool, shellCommandPrefix string) []agent.AgentTool {
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
func (pa *piAgent) createAcpReadFn(sessionID string) tools.ReadFileFn {
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
func (pa *piAgent) createAcpWriteFn(sessionID string) tools.WriteFileFn {
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
func (pa *piAgent) createAcpReadTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewReadToolWithReader(cwd, pa.createAcpReadFn(sessionID))
}

// createAcpWriteTool creates a write tool delegating to the ACP client.
func (pa *piAgent) createAcpWriteTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewWriteToolWithWriter(cwd, pa.createAcpWriteFn(sessionID))
}

// createAcpEditTool creates an edit tool delegating file I/O to the ACP client.
func (pa *piAgent) createAcpEditTool(cwd, sessionID string) agent.AgentTool {
	return tools.NewEditToolWithReadWriter(cwd,
		pa.createAcpReadFn(sessionID),
		pa.createAcpWriteFn(sessionID),
	)
}

func (pa *piAgent) createAcpBashTool(cwd, sessionID, shellCommandPrefix string) agent.AgentTool {
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

func (pa *piAgent) createBashOutputTool(sessionID string) agent.AgentTool {
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

func (pa *piAgent) createBashKillTool(sessionID string) agent.AgentTool {
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

func (pa *piAgent) handleEvent(sessionID string, entry *piSession, event core.AgentSessionEvent) {
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

func (pa *piAgent) handleSlashCommand(sessionID string, entry *piSession, command, args string) bool {
	switch command {
	case "compact":
		if _, err := entry.session.RunCompaction(context.Background(), args); err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Compaction failed: %v", err))
		} else {
			pa.sendAgentMessage(sessionID, "Session compacted successfully.")
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

func (pa *piAgent) handleResumeArg(sessionID string, entry *piSession, args string) {
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

func (pa *piAgent) handleLogout(sessionID string, entry *piSession, args string) {
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

func (pa *piAgent) handleLogin(sessionID string, entry *piSession, args string) {
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

func (pa *piAgent) sendAgentMessage(sessionID, text string) {
	_ = pa.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update:    acpsdk.UpdateAgentMessageText(text),
	})
}

func (pa *piAgent) sendAvailableCommands(sessionID string) {
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
