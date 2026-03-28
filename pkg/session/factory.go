package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session/store"
)

// SetupOptions configures a full session setup including MCP and extensions.
// All fields are optional — sensible defaults are derived from Cwd and AgentDir.
type SetupOptions struct {
	// Cwd is the working directory. Default: os.Getwd().
	Cwd string
	// AgentDir is the global config directory. Default: DefaultAgentDir().
	AgentDir string

	// AuthStorage for credentials. Default: created from AgentDir.
	AuthStorage *auth.AuthStorage
	// ModelRegistry for model lookup/key resolution. Default: created from AuthStorage.
	ModelRegistry *models.ModelRegistry
	// SettingsManager. Default: created from Cwd + AgentDir.
	SettingsManager *config.SettingsManager
	// SessionManager. Default: NewSessionManager(cwd, defaultSessionDir).
	SessionManager *store.SessionManager

	// Model to use. Default: from settings, else first available.
	Model *ai.Model
	// ThinkingLevel for reasoning. Default: from settings.
	ThinkingLevel string

	// Tools are the built-in tools. Default: DefaultCodingTools(cwd).
	Tools []agent.AgentTool

	// ResourceLoader. Default: created from ResourceLoaderOptions and Reload'd.
	ResourceLoader resources.ResourceLoader
	// ResourceLoaderOptions is used when ResourceLoader is nil.
	ResourceLoaderOptions *resources.ResourceLoaderOptions

	// UsageTracker records feature usage events. When nil, tracking is disabled.
	UsageTracker UsageTracker

	// CompactionRunner handles context compaction. When nil, compaction is disabled.
	CompactionRunner CompactionRunner

	// MCPConfigs are the MCP server configurations to start. When empty, no MCP.
	MCPConfigs map[string]mcp.ServerConfig
}

// SetupResult is returned by Setup.
type SetupResult struct {
	// Session is the created AgentSession.
	Session *AgentSession
	// ModelFallbackMessage is set if the session model couldn't be restored.
	ModelFallbackMessage string

	// Infrastructure handles returned for lifecycle management.
	MCPManager      *mcp.Manager
	ModelRegistry   *models.ModelRegistry
	SettingsManager *config.SettingsManager
	ResourceLoader  resources.ResourceLoader
	AgentDir        string
	Cwd             string
}

// Setup creates a fully wired session: resolves defaults, creates the agent
// session, starts MCP servers, wires channel messages, and optionally sets up
// extensions. This is the single path all modes should use.
func Setup(ctx context.Context, opts SetupOptions) (*SetupResult, error) {
	// --- Resolve defaults ---
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}

	agentDir := opts.AgentDir
	if agentDir == "" {
		if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
			agentDir = dir
		} else {
			agentDir = DefaultAgentDir()
		}
	}

	authStorage := opts.AuthStorage
	if authStorage == nil {
		authStorage = auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	}

	modelRegistry := opts.ModelRegistry
	if modelRegistry == nil {
		modelRegistry = models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	}

	settingsManager := opts.SettingsManager
	if settingsManager == nil {
		settingsManager = config.NewSettingsManager(cwd, agentDir)
	}

	sessionManager := opts.SessionManager
	if sessionManager == nil {
		sessionManager = store.NewSessionManager(cwd, store.DefaultSessionDir(agentDir, cwd))
	}

	// --- Resource loader ---
	rl := opts.ResourceLoader
	if rl == nil {
		rlopts := opts.ResourceLoaderOptions
		if rlopts == nil {
			rlopts = &resources.ResourceLoaderOptions{
				Cwd:             cwd,
				AgentDir:        agentDir,
				SettingsManager: settingsManager,
			}
		} else {
			// Ensure Cwd/AgentDir/SettingsManager are populated.
			if rlopts.Cwd == "" {
				rlopts.Cwd = cwd
			}
			if rlopts.AgentDir == "" {
				rlopts.AgentDir = agentDir
			}
			if rlopts.SettingsManager == nil {
				rlopts.SettingsManager = settingsManager
			}
		}
		rl = resources.NewResourceLoader(*rlopts)
		if err := rl.Reload(); err != nil {
			return nil, fmt.Errorf("reload resources: %w", err)
		}
	}

	// --- Tools ---
	toolList := opts.Tools

	// --- MCP ---
	var mcpMgr *mcp.Manager
	if len(opts.MCPConfigs) > 0 {
		mcpMgr = mcp.NewManager(opts.MCPConfigs, false)
		if toolList == nil {
			toolList = DefaultCodingTools(cwd)
		}
	}

	// --- Compaction ---
	compactionRunner := opts.CompactionRunner

	// --- Create agent session ---
	result, err := CreateAgentSession(ctx, CreateAgentSessionOptions{
		Cwd:              cwd,
		AgentDir:         agentDir,
		AuthStorage:      authStorage,
		ModelRegistry:    modelRegistry,
		Model:            opts.Model,
		ThinkingLevel:    opts.ThinkingLevel,
		SessionManager:   sessionManager,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		Tools:            toolList,
		CompactionRunner: compactionRunner,
		UsageTracker:     opts.UsageTracker,
	})
	if err != nil {
		if mcpMgr != nil {
			_ = mcpMgr.Close()
		}
		return nil, fmt.Errorf("create session: %w", err)
	}

	// --- Wire MCP channels ---
	if mcpMgr != nil {
		sess := result.Session
		mcp.WireChannelInjection(mcpMgr, func(text string, ts int64) {
			msg := agent.NewAgentMessage(ai.NewUserMsg(text, ts))
			sess.InjectMessage(msg)
		})

		// Snapshot base tools before MCP tools arrive.
		baseTools := slices.Clone(toolList)

		mcpMgr.OnToolsChanged.Store(func(mcpTools []agent.AgentTool) {
			merged := append(slices.Clone(baseTools), mcpTools...)

			// If hooks with tool interception are active, wrap before delivering.
			if hooks := sess.Hooks(); hooks != nil && (hooks.OnToolCall != nil || hooks.OnToolResult != nil) {
				merged = sess.WrapToolsWithHooks(merged)
			}

			sess.Agent.SetTools(merged)
		})

		mcpMgr.Start(ctx)
		firlog.Info("MCP servers starting", "servers", len(opts.MCPConfigs))
	}

	return &SetupResult{
		Session:              result.Session,
		ModelFallbackMessage: result.ModelFallbackMessage,
		MCPManager:           mcpMgr,
		ModelRegistry:        modelRegistry,
		SettingsManager:      settingsManager,
		ResourceLoader:       rl,
		AgentDir:             agentDir,
		Cwd:                  cwd,
	}, nil
}
