// ACP mode: expose fir as an ACP-compliant agent over stdio.
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/core/compaction"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
)

// version is set via SetVersion before RunAcpMode.
var version = "dev"

// SetVersion sets the version string for ACP mode responses.
func SetVersion(v string) { version = v }

// resolveAgentDir returns FIR_AGENT_DIR if set, otherwise the default agent directory.
func resolveAgentDir() string {
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		return dir
	}
	return core.DefaultAgentDir()
}

// firSession holds per-session state.
type firSession struct {
	session         *core.AgentSession
	modelRegistry   *models.ModelRegistry
	settingsManager *config.SettingsManager
	extSetup        *extension.SetupResult
	unsubscribe     func()
	cwd             string
	agentDir        string
	plan            *planTracker
	termState       *terminalState
	pendingArgs     sync.Map // toolCallID → map[string]any
	resumeMu        sync.Mutex
	lastResumeList  []session.SessionListInfo
	configAccessor  thinkingAccessor // nil → use session (for testing)
	mcpManager      *mcp.Manager     // nil if no MCP servers configured
}

// getThinkingAccessor returns the thinkingAccessor for this session.
// Uses configAccessor if set (tests), otherwise the AgentSession.
func (s *firSession) getThinkingAccessor() thinkingAccessor {
	if s.configAccessor != nil {
		return s.configAccessor
	}
	return s.session
}

// firAgent implements the ACP Agent interface.
type firAgent struct {
	conn    acpConn
	options Options

	mu          sync.Mutex
	sessions    map[string]*firSession
	clientCaps  acpsdk.ClientCapabilities
	authMethods []ExtendedAuthMethod
	authStorage *auth.AuthStorage // global auth storage from Initialize (same backing file as per-session instances)
}

// Compile-time interface check: firAgent must implement Agent.
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
		if entry.extSetup != nil {
			entry.extSetup.EmitSessionShutdown()
		}
		if entry.mcpManager != nil {
			_ = entry.mcpManager.Close()
		}
	}

	return nil
}

// ============================================================================
// Session creation
// ============================================================================

func (pa *firAgent) createSession(ctx context.Context, sessionID, cwd string, mcpConfigs map[string]mcp.ServerConfig) (*firSession, error) {
	agentDir := resolveAgentDir()

	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	settingsManager := config.NewSettingsManager(cwd, agentDir)
	sessionManager := session.NewSessionManager(cwd, session.DefaultSessionDir(agentDir, cwd))

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
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
		session:         result.Session,
		modelRegistry:   modelRegistry,
		settingsManager: settingsManager,
		cwd:             cwd,
		agentDir:        agentDir,
		plan:            &planTracker{conn: pa.conn, sessionID: sessionID},
		termState:       newTerminalState(),
		mcpManager:      mcpMgr,
	}

	unsub := result.Session.Subscribe(func(event core.AgentSessionEvent) {
		pa.handleEvent(sessionID, entry, event)
	})
	entry.unsubscribe = unsub

	// Extension setup — discover stdio-based extensions in .fir/extensions/
	if !pa.options.NoExtensions {
		extSetup, err := extension.Setup(result.Session, extension.SetupOptions{
			ProjectDir:   cwd,
			Cwd:          cwd,
			Mode:         "acp",
			EnabledNames: resolveEnabledExtensions(pa.options.EnabledExtensions, settingsManager),
		})
		if err == nil && extSetup != nil {
			entry.extSetup = extSetup
			extSetup.EmitSessionStart()
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
	if entry.settingsManager == nil || entry.settingsManager.GetEnableSkillCommands() {
		skills, _ := entry.session.ResourceLoader().GetSkills()
		for _, s := range skills {
			desc := s.Description
			if desc == "" {
				desc = "Skill: " + s.Name
			}
			commands = append(commands, acpsdk.AvailableCommand{Name: "skill:" + s.Name, Description: desc})
		}
	}

	// Add extension commands
	if entry.extSetup != nil && entry.extSetup.Manager != nil {
		for _, ec := range entry.extSetup.Manager.GetCommands() {
			desc := ec.Spec.Description
			if desc == "" {
				desc = "Extension command"
			}
			commands = append(commands, acpsdk.AvailableCommand{Name: ec.Spec.Name, Description: desc})
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

// isNotFound reports whether an exec error is "command not found".
func isNotFound(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	return true // LookupError / exec.ErrNotFound etc.
}

// resolveEnabledExtensions merges the CLI --extension names with the
// project/global settings "extensions" list. When the combined list is empty,
// all discovered extensions are started. The caller must check NoExtensions
// before calling this.
func resolveEnabledExtensions(cliNames []string, sm *config.SettingsManager) []string {
	seen := make(map[string]bool)
	var names []string
	for _, n := range sm.GetEnabledExtensions() {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	for _, n := range cliNames {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	return names
}

// gistIDRegex matches a valid GitHub Gist ID (hex string of at least 20 characters).
var gistIDRegex = regexp.MustCompile(`^[a-fA-F0-9]{20,}$`)

// providerIDRegex validates provider IDs (alphanumeric with hyphens).
var providerIDRegex = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)
