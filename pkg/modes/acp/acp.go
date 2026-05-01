// ACP mode: expose fir as an ACP-compliant agent over stdio.
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/compaction"
	"github.com/kfet/fir/pkg/session/store"
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
	return session.DefaultAgentDir()
}

// firSession holds per-session state.
type firSession struct {
	session          *session.AgentSession
	modelRegistry    *models.ModelRegistry
	settingsManager  *config.SettingsManager
	extSetup         *extension.SetupResult
	unsubscribe      func()
	cwd              string
	agentDir         string
	plan             *planTracker
	termState        *terminalState
	pendingArgs      sync.Map // toolCallID → map[string]any
	resumeMu         sync.Mutex
	lastResumeList   []store.SessionListInfo
	configAccessor   thinkingAccessor            // nil → use session (for testing)
	mcpManager       *mcp.Manager                // nil if no MCP servers configured; used for Close()
	mcpStatus        func() []mcp.ServerStatus   // status callback for /session display
	extReady         chan struct{}               // closed when async extension setup completes
	clientMCPConfigs map[string]mcp.ServerConfig // MCP configs from ACP client request, re-merged on reload
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
	conn     acpConn
	options  Options
	commands *commandRegistry

	mu          sync.Mutex
	sessions    map[string]*firSession
	clientCaps  acpsdk.ClientCapabilities
	authMethods []ExtendedAuthMethod
	authStorage *auth.AuthStorage // global auth storage from Initialize, shared by all sessions
	// authExtSetup holds auth-provider extensions started eagerly in
	// Initialize so their OAuth providers are registered before
	// authMethods is built. Sessions exclude these names from their own
	// extension startup to avoid double-starting.
	authExtSetup *extension.AuthSetupResult

	// pendingAuths tracks in-flight interactive OAuth logins, keyed by
	// the per-flow opaque id returned to the client on call 1. Multiple
	// concurrent flows per auth method are supported. Used by the
	// two-call `_meta.auth.interactive` protocol so a relay (e.g.
	// poe-acp-relay) can surface the auth URL to a remote user and feed
	// the pasted redirect URL back into the login flow.
	pendingAuths map[string]*pendingAuth
}

// Compile-time interface check: firAgent must implement Agent.
var _ acpsdk.Agent = (*firAgent)(nil)

// builtInCommands returns the slash commands available in ACP mode
// by reading them from the global command registry.
func builtInCommands() []acpsdk.AvailableCommand {
	return newCommandRegistry().availableCommands()
}

// RunAcpMode is the entry point for ACP mode over stdin/stdout.
func RunAcpMode(opts Options) error {
	runStart := time.Now()
	firlog.Info("acp server starting")
	pa := &firAgent{
		options:      opts,
		sessions:     make(map[string]*firSession),
		commands:     newCommandRegistry(),
		pendingAuths: make(map[string]*pendingAuth),
	}

	// Use newRawConn instead of AgentSideConnection so that session/list and
	// session/resume (not in the Go SDK's stable dispatch) are handled correctly.
	conn, done := newRawConn(pa, os.Stdout, os.Stdin)
	pa.conn = conn
	firlog.Info("acp server: connection established", "elapsed_ms", time.Since(runStart).Milliseconds())

	// Catch SIGTERM/SIGINT so we still run cleanup when the host kills us.
	// SIGHUP is intentionally not trapped — it takes its default action
	// (terminate) so tmux/ssh hangups cleanly tear down the process
	// rather than re-execing it and leaking MCP subprocesses.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Block until connection closes or we receive a signal.
	select {
	case <-done:
	case sig := <-sigCh:
		firlog.Info("acp received signal, shutting down", "signal", sig)
	}
	signal.Stop(sigCh)

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
		if entry.extReady != nil {
			<-entry.extReady
		}
		if entry.extSetup != nil {
			entry.extSetup.EmitSessionShutdown()
		}
		if entry.mcpManager != nil {
			_ = entry.mcpManager.Close()
		}
	}

	// Stop the auth-provider extensions started in Initialize.
	pa.mu.Lock()
	authExt := pa.authExtSetup
	pa.authExtSetup = nil
	pa.mu.Unlock()
	if authExt != nil {
		authExt.Stop()
	}

	return nil
}

// ============================================================================
// Session creation
// ============================================================================

func (pa *firAgent) createSession(ctx context.Context, sessionID, cwd string, mcpConfigs map[string]mcp.ServerConfig) (*firSession, error) {
	createStart := time.Now()
	firlog.Info("acp createSession: start", "sessionID", sessionID, "cwd", cwd)

	agentDir := resolveAgentDir()

	// Reuse the global authStorage created in Initialize so that login/logout
	// changes are immediately visible to all sessions without a Reload().
	pa.mu.Lock()
	authStorage := pa.authStorage
	pa.mu.Unlock()
	if authStorage == nil {
		authStorage = auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	}

	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	settingsManager := config.NewSettingsManager(cwd, agentDir)

	// Create tools — use ACP delegation based on client capabilities.
	pa.mu.Lock()
	caps := pa.clientCaps
	pa.mu.Unlock()
	useClientTerminal := caps.Terminal
	useClientFs := caps.Fs.WriteTextFile
	shellCommandPrefix := settingsManager.GetShellCommandPrefix()

	var toolList []agent.AgentTool
	if useClientTerminal || useClientFs {
		toolList = pa.createAcpTools(cwd, sessionID, useClientTerminal, useClientFs, shellCommandPrefix)
	} else {
		if shellCommandPrefix != "" {
			toolList = session.DefaultCodingToolsWithPrefix(cwd, shellCommandPrefix)
		} else {
			toolList = session.DefaultCodingTools(cwd)
		}
	}
	firlog.Info("acp createSession: tools created", "count", len(toolList), "clientTerminal", useClientTerminal, "clientFs", useClientFs)

	result, err := session.Setup(ctx, session.SetupOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		SettingsManager: settingsManager,
		SessionStore:    store.NewSessionStore(cwd, store.DefaultSessionDir(agentDir, cwd)),
		Tools:           toolList,
		MCPConfigs:      mcpConfigs,
		OnMCPServerReady: func(name string, err error) {
			if err != nil {
				pa.sendAgentMessage(sessionID, fmt.Sprintf("⚠️ MCP server %q failed to connect: %v", name, err))
			} else {
				pa.sendAgentMessage(sessionID, fmt.Sprintf("MCP server %q connected", name))
			}
		},
		ResourceLoaderOptions: &resources.ResourceLoaderOptions{
			Cwd:                           cwd,
			AgentDir:                      agentDir,
			SettingsManager:               settingsManager,
			NoSkills:                      pa.options.NoSkills,
			AdditionalSkillPaths:          pa.options.AdditionalSkillPaths,
			AdditionalPromptTemplatePaths: pa.options.AdditionalPromptTemplatePaths,
			NoPromptTemplates:             pa.options.NoPromptTemplates,
		},
		CompactionRunner: &compaction.DefaultRunner{
			SettingsManager: settingsManager,
			ModelRegistry:   modelRegistry,
		},
	})
	if err != nil {
		return nil, err
	}

	entry := &firSession{
		session:         result.Session,
		modelRegistry:   result.ModelRegistry,
		settingsManager: result.SettingsManager,
		cwd:             result.Cwd,
		agentDir:        result.AgentDir,
		plan:            &planTracker{conn: pa.conn, sessionID: sessionID},
		termState:       newTerminalState(),
		mcpManager:      result.MCPManager,
		mcpStatus:       mcp.StatusFunc(result.MCPManager),
	}

	// --wait-mcp: block session creation until every MCP server has finished
	// its initial handshake so the first prompt sees all MCP tools.
	if pa.options.WaitMCP && entry.mcpManager != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if werr := entry.mcpManager.WaitReady(waitCtx); werr != nil {
			firlog.Warn("acp createSession: wait-mcp timed out", "err", werr)
		}
		cancel()
	}

	unsub := result.Session.Subscribe(func(event session.AgentSessionEvent) {
		pa.handleEvent(sessionID, entry, event)
	})
	entry.unsubscribe = unsub

	// Extension setup — start eager extensions synchronously (auth extensions
	// must be ready before session/new responds with models), but fire
	// session_start (lazy extensions) asynchronously.
	entry.extReady = make(chan struct{})
	if !pa.options.NoExtensions {
		t0 := time.Now()
		pa.mu.Lock()
		authExtNames := []string(nil)
		if pa.authExtSetup != nil {
			authExtNames = append(authExtNames, pa.authExtSetup.Names...)
		}
		pa.mu.Unlock()
		// Don't re-start auth-provider extensions; they're already running
		// from Initialize and their auth providers are globally registered.
		disabled := append([]string(nil), pa.options.DisabledExtensions...)
		disabled = append(disabled, authExtNames...)
		extSetup, err := extension.Setup(result.Session, extension.SetupOptions{
			ProjectDir:    cwd,
			Cwd:           cwd,
			Mode:          "acp",
			Version:       version,
			EnabledNames:  resolveEnabledExtensions(pa.options.EnabledExtensions, result.SettingsManager),
			DisabledNames: disabled,
			ConfigDirs:    []string{filepath.Join(cwd, ".fir"), resolveAgentDir()},
		})
		firlog.Info("acp createSession: extension setup (eager)", "elapsed_ms", time.Since(t0).Milliseconds())
		if err == nil && extSetup != nil {
			entry.extSetup = extSetup
			go func() {
				t1 := time.Now()
				extSetup.EmitSessionStart(nil)
				firlog.Info("acp createSession: EmitSessionStart (async)", "elapsed_ms", time.Since(t1).Milliseconds())
				close(entry.extReady)
			}()
		} else {
			close(entry.extReady)
		}
	} else {
		close(entry.extReady)
	}

	pa.mu.Lock()
	pa.sessions[sessionID] = entry
	pa.mu.Unlock()

	firlog.Info("acp createSession: done", "total_ms", time.Since(createStart).Milliseconds(), "sessionID", sessionID)
	return entry, nil
}

// loadProjectMCPConfigs reads MCP server configurations from both the
// user-level and project-level config files and merges them (project wins).
// Returns nil if no configs are found. Logs a warning to stderr if a config
// file exists but cannot be read or parsed.
func loadProjectMCPConfigs(cwd, extraConfigPath string) map[string]mcp.ServerConfig {
	cfg, err := mcp.LoadDefaultConfigs(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fir: warning: %v — no MCP servers will be started\n", err)
		return nil
	}
	if extraConfigPath != "" {
		extra, extraErr := mcp.LoadConfigFile(extraConfigPath)
		if extraErr != nil {
			fmt.Fprintf(os.Stderr, "fir: warning: failed to load MCP config %s: %v\n", extraConfigPath, extraErr)
		} else {
			cfg = mcp.MergeConfigs(cfg, extra)
		}
	}
	if cfg == nil {
		return nil
	}
	return cfg.MCPServers
}

// ============================================================================
// Helpers
// ============================================================================

// mergeRequestMCPServers merges ACP request-level MCP server configurations
// into an existing config map. Request entries take precedence. Returns the
// merged map (creating one if configs is nil and servers is non-empty).
func mergeRequestMCPServers(configs map[string]mcp.ServerConfig, servers []acpsdk.McpServer) map[string]mcp.ServerConfig {
	if len(servers) == 0 {
		return configs
	}
	if configs == nil {
		configs = make(map[string]mcp.ServerConfig)
	}
	for _, mcpServer := range servers {
		switch {
		case mcpServer.Stdio != nil:
			envs := map[string]string{}
			for _, v := range mcpServer.Stdio.Env {
				envs[v.Name] = v.Value
			}
			configs[mcpServer.Stdio.Name] = mcp.ServerConfig{
				Command: mcpServer.Stdio.Command,
				Args:    mcpServer.Stdio.Args,
				Env:     envs,
			}
		case mcpServer.Http != nil:
			if len(mcpServer.Http.Headers) > 0 {
				fmt.Fprintf(os.Stderr, "fir: warning: MCP server %q specifies HTTP headers which are not yet supported; headers will be ignored\n", mcpServer.Http.Name)
			}
			configs[mcpServer.Http.Name] = mcp.ServerConfig{
				Transport: "streamable",
				URL:       mcpServer.Http.Url,
			}
		case mcpServer.Sse != nil:
			if len(mcpServer.Sse.Headers) > 0 {
				fmt.Fprintf(os.Stderr, "fir: warning: MCP server %q specifies HTTP headers which are not yet supported; headers will be ignored\n", mcpServer.Sse.Name)
			}
			configs[mcpServer.Sse.Name] = mcp.ServerConfig{
				Transport: "sse",
				URL:       mcpServer.Sse.Url,
			}
		default:
			fmt.Fprintf(os.Stderr, "fir: warning: MCP server has unknown transport; skipping\n")
		}
	}
	return configs
}

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

	// Wait for async extension setup so extension commands are included.
	if entry.extReady != nil {
		<-entry.extReady
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
