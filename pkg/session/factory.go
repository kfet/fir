package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/mcp/autoreply"
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
	// SessionStore. Default: NewSessionStore(cwd, defaultSessionDir).
	SessionStore *store.SessionStore

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

	// OnRetry is forwarded to the agent and invoked before a retryable
	// pre-stream provider error (rate limit / overloaded / transient 5xx)
	// is retried.
	OnRetry func(attempt int, delaySeconds float64, errMsg string)

	// ExtReady is closed when extensions finish loading. Live model fetching
	// for OAuth providers waits on this. When nil, OAuth fetching starts immediately.
	ExtReady <-chan struct{}
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

	sessionStore := opts.SessionStore
	if sessionStore == nil {
		sessionStore = store.NewSessionStore(cwd, store.DefaultSessionDir(agentDir, cwd))
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
	// Ensure tools exist when MCP servers need to register their tools.
	if len(opts.MCPConfigs) > 0 && toolList == nil {
		toolList = DefaultCodingTools(cwd)
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
		SessionStore:     sessionStore,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		Tools:            toolList,
		CompactionRunner: compactionRunner,
		UsageTracker:     opts.UsageTracker,
		ExtReady:         opts.ExtReady,
		OnRetry:          opts.OnRetry,
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// --- Wire and start MCP servers ---
	// Lifecycle events (connecting/ready/disconnected) are surfaced via the
	// Manager's buffered ServerEvents channel; callers attach a consumer to
	// mcpMgr after Setup returns.
	mcpMgr := StartMCPManager(ctx, result.Session, opts.MCPConfigs)

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

// StartMCPManager creates a new MCP Manager, wires it to the given session
// (channel injection + tool-change callback), and starts it.  This is the
// same wiring that Setup performs internally and is exported so that callers
// (interactive /reload, ACP /reload) can spin up MCP after initial setup.
// Server lifecycle events are surfaced via the Manager's ServerEvents channel.
func StartMCPManager(ctx context.Context, sess *AgentSession, configs map[string]mcp.ServerConfig) *mcp.Manager {
	if len(configs) == 0 {
		return nil
	}
	mgr := mcp.NewManager(configs, false)

	// Auto-reply: when a channel message arrives from a message_id-addressed
	// reply tool, stream LLM output directly without manual reply() calls.
	var ar *autoreply.State
	replyHook := func(serverName, messageID string) {
		firlog.Info("auto-reply: replyHook fired", "server", serverName, "messageID", messageID)
		if ar == nil {
			ar = autoreply.New(func(ctx context.Context, args map[string]any) error {
				_, err := mgr.CallTool(ctx, serverName, "reply", args)
				if err != nil {
					firlog.Info("auto-reply: CallTool error", "err", err)
				}
				return err
			})
			ar.Wire(sess.Agent)
			firlog.Info("auto-reply: wired to agent")
		}
		ar.SetMessageID(messageID)
	}

	mcp.WireChannelInjectionWithReplyHook(mgr, func(content any, ts int64) {
		// AgentSession.InjectMessage waits on ExtReady internally before
		// firing an LLM call, so we don't need an outer gate here.
		msg := agent.NewAgentMessage(ai.NewUserMsg(content, ts))
		sess.InjectMessage(msg)
	}, replyHook, func() int {
		return len(sess.SessionStore.BuildSessionContext().Messages)
	})

	var prevMCPNames []string
	mgr.SetOnToolsChanged(func(mcpTools []agent.AgentTool) {
		if hooks := sess.Hooks(); hooks != nil && (hooks.OnToolCall != nil || hooks.OnToolResult != nil) {
			mcpTools = sess.WrapToolsWithHooks(mcpTools)
		}

		// Wrap reply tools to intercept manual calls when auto-reply is active.
		for i, t := range mcpTools {
			if !strings.HasSuffix(t.Name, "reply") {
				continue
			}
			origExec := t.Execute
			mcpTools[i].Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
				if ar != nil {
					if absorbed, _ := ar.InterceptReply(ctx, params); absorbed {
						return agent.AgentToolResult{Content: []ai.ToolResultContent{{Type: "text", Text: "ok (auto-reply active)"}}}, nil
					}
				}
				return origExec(ctx, toolCallID, params, onUpdate)
			}
			// Update description to inform LLM that auto-reply is active.
			mcpTools[i].Description += "\n\nNOTE: Auto-reply is active — your text output streams to the user automatically. Do NOT call this tool for normal responses. Only use for replace=true (progress updates) or error=true (error signalling)."
		}

		sess.Agent.UpdateTools(func(ts *agent.ToolSet) {
			for _, name := range prevMCPNames {
				ts.Remove(name)
			}
			for _, t := range mcpTools {
				ts.Add(t)
			}
		})
		names := make([]string, len(mcpTools))
		for i, t := range mcpTools {
			names[i] = t.Name
		}
		prevMCPNames = names
	})

	mgr.Start(ctx)
	firlog.Info("MCP servers starting", "servers", len(configs))
	return mgr
}

// ReloadMCP re-reads MCP configs from disk, merges an optional extra config
// file and additional in-memory overrides, then either reloads an existing
// manager or creates+wires a new one.  mgrPtr is updated in place so callers
// see the new manager.  cwd is the project working directory; extraConfigPath
// is an additional config file (e.g. from --mcp-config), pass "" to skip.
// extraConfigs are additional server configs (e.g. client-provided via ACP)
// that are merged last (highest precedence).
func ReloadMCP(ctx context.Context, mgrPtr **mcp.Manager, sess *AgentSession, cwd, extraConfigPath string, extraConfigs map[string]mcp.ServerConfig) error {
	cfg, err := mcp.LoadDefaultConfigs(cwd)
	if err != nil {
		return fmt.Errorf("load MCP config: %w", err)
	}
	if extraConfigPath != "" {
		extra, extraErr := mcp.LoadConfigFile(extraConfigPath)
		if extraErr != nil {
			return fmt.Errorf("load extra MCP config %s: %w", extraConfigPath, extraErr)
		}
		cfg = mcp.MergeConfigs(cfg, extra)
	}
	servers := cfg.MCPServers
	// Merge in-memory overrides (e.g. ACP client-provided MCP servers).
	for name, sc := range extraConfigs {
		if servers == nil {
			servers = make(map[string]mcp.ServerConfig)
		}
		servers[name] = sc
	}
	if *mgrPtr != nil {
		_, err = (*mgrPtr).Reload(ctx, servers)
		return err
	}
	// No manager yet — create one if configs appeared.
	if len(servers) > 0 {
		*mgrPtr = StartMCPManager(ctx, sess, servers)
	}
	return nil
}
