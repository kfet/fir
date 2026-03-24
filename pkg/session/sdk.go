// Ported from: packages/coding-agent/src/core/sdk.ts
// Upstream hash: 4ba3e5be
package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Types
// ============================================================================

// CreateAgentSessionOptions configures createAgentSession.
type CreateAgentSessionOptions struct {
	// Cwd is the working directory. Default: os.Getwd().
	Cwd string
	// AgentDir is the global config directory. Default: ~/.config/fir
	AgentDir string

	// AuthStorage for credentials. Default: auth.NewAuthStorage(agentDir/auth.json)
	AuthStorage *auth.AuthStorage
	// ModelRegistry for model lookup/key resolution. Default: created from AuthStorage.
	ModelRegistry *models.ModelRegistry

	// Model to use. Default: from settings, else first available.
	Model *ai.Model
	// ThinkingLevel for reasoning. Default: from settings, else "medium".
	ThinkingLevel string

	// Tools are the built-in tools to use. Default: coding tools [read, bash, edit, write].
	Tools []agent.AgentTool

	// ResourceLoader. When nil, DefaultResourceLoader is created and Reload'd.
	ResourceLoader resources.ResourceLoader

	// SessionManager. Default: SessionManager.Create(cwd).
	SessionManager *store.SessionManager

	// SettingsManager. Default: SettingsManager.Create(cwd, agentDir).
	SettingsManager *config.SettingsManager

	// CompactionRunner handles context compaction. When nil, compaction is disabled.
	CompactionRunner CompactionRunner

	// UsageTracker records feature usage events. When nil, tracking is disabled.
	UsageTracker UsageTracker
}

// CreateAgentSessionResult is returned by CreateAgentSession.
type CreateAgentSessionResult struct {
	// Session is the created AgentSession.
	Session *AgentSession
	// ModelFallbackMessage is set if the session model couldn't be restored.
	ModelFallbackMessage string
}

// ============================================================================
// Factory function
// ============================================================================

// CreateAgentSession creates a fully wired AgentSession from the given options.
func CreateAgentSession(ctx context.Context, opts CreateAgentSessionOptions) (*CreateAgentSessionResult, error) {
	cwd := opts.Cwd
	if cwd == "" {
		cwd = "."
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve cwd: %w", err)
	}
	cwd = absCwd

	agentDir := opts.AgentDir
	if agentDir == "" {
		agentDir = DefaultAgentDir()
	}

	// Auth & model registry
	authStorage := opts.AuthStorage
	if authStorage == nil {
		authStorage = auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	}

	modelRegistry := opts.ModelRegistry
	if modelRegistry == nil {
		modelRegistry = models.NewModelRegistry(authStorage, models.DefaultModelsJsonPath(agentDir))
	}

	// Start background fetch of live model lists from provider APIs.
	modelRegistry.StartLiveModelFetch(ctx, filepath.Join(agentDir, "cache"))

	// Settings
	settingsManager := opts.SettingsManager
	if settingsManager == nil {
		settingsManager = config.NewSettingsManager(cwd, agentDir)
	}

	// Session
	sessionManager := opts.SessionManager
	if sessionManager == nil {
		sessionManager = store.NewSessionManager(cwd, store.DefaultSessionDir(agentDir, cwd))
	}

	// Resources
	resourceLoader := opts.ResourceLoader
	if resourceLoader == nil {
		rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
			Cwd:             cwd,
			AgentDir:        agentDir,
			SettingsManager: settingsManager,
		})
		if err := rl.Reload(); err != nil {
			return nil, fmt.Errorf("reload resources: %w", err)
		}
		resourceLoader = rl
	}

	// Build session context to check for existing data
	existingSession := sessionManager.BuildSessionContext()
	hasExistingSession := len(existingSession.Messages) > 0

	// Resolve model
	model := opts.Model
	var modelFallbackMessage string

	if model == nil && hasExistingSession && existingSession.Model != nil {
		restored := modelRegistry.Find(existingSession.Model.Provider, existingSession.Model.ModelID)
		if restored != nil && modelRegistry.GetApiKey(restored) != "" {
			model = restored
		}
		if model == nil {
			modelFallbackMessage = fmt.Sprintf("Could not restore model %s/%s",
				existingSession.Model.Provider, existingSession.Model.ModelID)
		}
	}

	if model == nil {
		result := models.FindInitialModel(models.FindInitialModelOptions{
			IsContinuing:         hasExistingSession,
			DefaultProvider:      settingsManager.GetDefaultProvider(),
			DefaultModelID:       settingsManager.GetDefaultModel(),
			DefaultThinkingLevel: settingsManager.GetDefaultThinkingLevel(),
			ModelRegistry:        modelRegistry,
		})
		model = result.Model
		if model == nil {
			if modelFallbackMessage == "" {
				modelFallbackMessage = "No models available. Use /login or set an API key environment variable."
			}
		} else if modelFallbackMessage != "" {
			modelFallbackMessage += fmt.Sprintf(". Using %s/%s", model.Provider, model.ID)
		}
	}

	// Resolve thinking level
	thinkingLevel := opts.ThinkingLevel
	if thinkingLevel == "" && hasExistingSession {
		if existingSession.ThinkingLevel != "" {
			thinkingLevel = existingSession.ThinkingLevel
		} else {
			tl := settingsManager.GetDefaultThinkingLevel()
			if tl == "" {
				tl = string(config.DefaultThinkingLevel)
			}
			thinkingLevel = tl
		}
	}
	if thinkingLevel == "" {
		tl := settingsManager.GetDefaultThinkingLevel()
		if tl == "" {
			tl = string(config.DefaultThinkingLevel)
		}
		thinkingLevel = tl
	}
	if model == nil || !model.Reasoning {
		thinkingLevel = "off"
	}

	// Resolve tools
	agentTools := opts.Tools
	if agentTools == nil {
		agentTools = DefaultCodingTools(cwd)
	}

	// Create agent
	agentOpts := agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "",
			Model:         model,
			ThinkingLevel: agent.ThinkingLevel(thinkingLevel),
			Tools:         agent.ToolSetFrom(agentTools),
		},
		ConvertToLLM: func(messages []agent.AgentMessage) ([]ai.Message, error) {
			return store.ConvertToLLM(messages)
		},
		SessionID:    sessionManager.GetSessionID(),
		SteeringMode: settingsManager.GetSteeringMode(),
		FollowUpMode: settingsManager.GetFollowUpMode(),
		Transport:    ai.Transport(settingsManager.GetTransport()),
		GetApiKey: func(provider string) (string, error) {
			resolvedProvider := provider
			if resolvedProvider == "" && model != nil {
				resolvedProvider = model.Provider
			}
			if resolvedProvider == "" {
				return "", fmt.Errorf("no model selected")
			}
			key := modelRegistry.GetApiKeyForProvider(resolvedProvider)
			if key == "" {
				if keyErr := modelRegistry.GetApiKeyError(resolvedProvider); keyErr != nil {
					return "", fmt.Errorf(
						"authentication failed for %q: %v. Run '/login %s' to re-authenticate",
						resolvedProvider, keyErr, resolvedProvider)
				}
				if model != nil && modelRegistry.IsUsingOAuth(model) {
					return "", fmt.Errorf(
						"authentication failed for %q. Credentials may have expired. Run '/login %s' to re-authenticate",
						resolvedProvider, resolvedProvider)
				}
				return "", fmt.Errorf(
					"no API key found for %q. Set an API key environment variable or run '/login %s'",
					resolvedProvider, resolvedProvider)
			}
			return key, nil
		},
	}

	// Configure server tools from settings (Anthropic only).
	if serverToolNames := settingsManager.GetServerTools(); len(serverToolNames) > 0 {
		agentOpts.ServerTools = ResolveServerTools(serverToolNames)
	}

	// Configure server-side compaction from settings (Anthropic only).
	if sc := settingsManager.GetServerCompaction(); sc != nil && sc.Enabled != nil && *sc.Enabled {
		c := &ai.AnthropicCompaction{Enabled: true}
		if sc.TriggerTokens != nil {
			c.TriggerTokens = *sc.TriggerTokens
		}
		c.Instructions = sc.Instructions
		agentOpts.Compaction = c
	}

	a := agent.NewAgent(agentOpts)

	// Restore messages or record initial state
	if hasExistingSession {
		a.ReplaceMessages(existingSession.Messages)
	} else {
		if model != nil {
			sessionManager.AppendModelChange(model.Provider, model.ID)
		}
		sessionManager.AppendThinkingLevelChange(thinkingLevel)
	}

	// Create AgentSession
	session := NewAgentSession(AgentSessionOptions{
		Agent:            a,
		SessionManager:   sessionManager,
		SettingsManager:  settingsManager,
		ResourceLoader:   resourceLoader,
		ModelRegistry:    modelRegistry,
		CompactionRunner: opts.CompactionRunner,
		UsageTracker:     opts.UsageTracker,
		Cwd:              cwd,
	})

	// Register session-aware tools (plan tool needs a session reference).
	session.RegisterSessionTools()

	// Restore plan state from an existing session without writing a new entry.
	if hasExistingSession {
		session.restorePlan(existingSession.PlanTitle, existingSession.PlanEntries, existingSession.PlanMetadata)
	}

	return &CreateAgentSessionResult{
		Session:              session,
		ModelFallbackMessage: modelFallbackMessage,
	}, nil
}

// ============================================================================
// Helpers
// ============================================================================

// DefaultAgentDir returns the default global config directory (~/.config/fir).
// Respects $XDG_CONFIG_HOME if set.
func DefaultAgentDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fir")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fir")
}

// LegacyFirAgentDir returns the old global config directory (~/.fir/agent)
// so that sessions and data created by older fir versions remain accessible.
func LegacyFirAgentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".fir", "agent")
}

// PiAgentDir returns the pi agent directory (~/.pi/agent) for picking up
// sessions created by pi (Claude Code).
func PiAgentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

// MigrateConfigFromLegacyDir copies config files (not sessions/cache) from
// ~/.fir/agent/ to the new config dir (~/.config/fir/) if the new dir doesn't
// exist yet. This is a one-time copy; the legacy dir is left intact so that
// older fir versions on other branches continue to work.
func MigrateConfigFromLegacyDir() {
	newDir := DefaultAgentDir()
	legacyDir := LegacyFirAgentDir()

	// If new dir already exists, nothing to do.
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	// If legacy dir doesn't exist, nothing to migrate.
	if _, err := os.Stat(legacyDir); err != nil {
		return
	}

	// Config files to copy (not sessions/ or cache/ — those stay in legacy).
	configFiles := []string{
		"settings.json",
		"keybindings.json",
		"auth.json",
		"models.json",
		"usage.json",
	}

	// The legacy mcp.json lived at ~/.fir/mcp.json (not inside agent/).
	home, _ := os.UserHomeDir()
	legacyMcpPath := filepath.Join(home, ".fir", "mcp.json")

	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return
	}

	for _, name := range configFiles {
		src := filepath.Join(legacyDir, name)
		copyFileIfExists(src, filepath.Join(newDir, name))
	}
	// Copy mcp.json from its legacy location (~/.fir/mcp.json).
	copyFileIfExists(legacyMcpPath, filepath.Join(newDir, "mcp.json"))

	// Copy config subdirectories (skills, extensions, prompts).
	for _, subdir := range []string{"skills", "extensions", "prompts"} {
		src := filepath.Join(legacyDir, subdir)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			copyDirRecursive(src, filepath.Join(newDir, subdir))
		}
	}
}

// copyFileIfExists copies src to dst if src exists. Does not overwrite dst.
func copyFileIfExists(src, dst string) {
	if _, err := os.Stat(dst); err == nil {
		return // don't overwrite
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.WriteFile(dst, data, 0o644)
}

// copyDirRecursive copies a directory tree. Does not overwrite existing files.
func copyDirRecursive(src, dst string) {
	_ = os.MkdirAll(dst, 0o755)
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDirRecursive(srcPath, dstPath)
		} else {
			copyFileIfExists(srcPath, dstPath)
		}
	}
}

// DefaultCodingTools creates the standard set of coding tools for a cwd.
func DefaultCodingTools(cwd string) []agent.AgentTool {
	return []agent.AgentTool{
		tools.NewReadTool(cwd),
		tools.NewBashTool(cwd),
		tools.NewEditTool(cwd),
		tools.NewWriteTool(cwd),
	}
}

// DefaultCodingToolsWithPrefix creates the standard set of coding tools
// with a shell command prefix prepended to every bash command.
func DefaultCodingToolsWithPrefix(cwd, prefix string) []agent.AgentTool {
	return []agent.AgentTool{
		tools.NewReadTool(cwd),
		tools.NewBashToolWithPrefix(cwd, prefix),
		tools.NewEditTool(cwd),
		tools.NewWriteTool(cwd),
	}
}

// AllTools creates all available tools for a cwd.
func AllTools(cwd string) []agent.AgentTool {
	return []agent.AgentTool{
		tools.NewReadTool(cwd),
		tools.NewBashTool(cwd),
		tools.NewEditTool(cwd),
		tools.NewWriteTool(cwd),
		tools.NewGrepTool(cwd),
		tools.NewFindTool(cwd),
		tools.NewLsTool(cwd),
	}
}

// serverToolTypeMap maps short names to Anthropic server tool type identifiers.
// Uses basic versions that work without code execution dependencies.
// For dynamic filtering versions, use the raw type identifiers (e.g. "web_search_20260209").
var serverToolTypeMap = map[string]string{
	"web_search":     "web_search_20250305",
	"web_fetch":      "web_fetch_20250910",
	"code_execution": "code_execution_20250825",
}

// ResolveServerTools converts short tool names (from settings) to AnthropicServerTool structs.
// Deduplicates code_execution when dynamic-filtering tool versions (20260209) auto-inject it.
func ResolveServerTools(names []string) []ai.AnthropicServerTool {
	var tools []ai.AnthropicServerTool
	hasDynamicFiltering := false
	hasExplicitCodeExec := false

	for _, name := range names {
		toolType, ok := serverToolTypeMap[name]
		if !ok {
			toolType = name
		}
		if strings.HasPrefix(toolType, "web_search_20260") || strings.HasPrefix(toolType, "web_fetch_20260") {
			hasDynamicFiltering = true
		}
		if strings.HasPrefix(toolType, "code_execution") {
			hasExplicitCodeExec = true
		}
		tools = append(tools, ai.AnthropicServerTool{Type: toolType})
	}

	// Dynamic filtering tool versions auto-inject code_execution server-side.
	// Remove our explicit code_execution to avoid name conflicts.
	if hasDynamicFiltering && hasExplicitCodeExec {
		filtered := tools[:0]
		for _, t := range tools {
			if !strings.HasPrefix(t.Type, "code_execution") {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	return tools
}
