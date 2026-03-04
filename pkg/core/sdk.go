// Ported from: packages/coding-agent/src/core/sdk.ts
// Upstream hash: 4ba3e5be
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core/tools"
)

// ============================================================================
// Types
// ============================================================================

// CreateAgentSessionOptions configures createAgentSession.
type CreateAgentSessionOptions struct {
	// Cwd is the working directory. Default: os.Getwd().
	Cwd string
	// AgentDir is the global config directory. Default: ~/.fir/agent
	AgentDir string

	// AuthStorage for credentials. Default: NewAuthStorage(agentDir/auth.json)
	AuthStorage *AuthStorage
	// ModelRegistry for model lookup/key resolution. Default: created from AuthStorage.
	ModelRegistry *ModelRegistry

	// Model to use. Default: from settings, else first available.
	Model *ai.Model
	// ThinkingLevel for reasoning. Default: from settings, else "medium".
	ThinkingLevel string
	// ScopedModels available for model cycling.
	ScopedModels []ScopedModel

	// Tools are the built-in tools to use. Default: coding tools [read, bash, edit, write].
	Tools []agent.AgentTool

	// ResourceLoader. When nil, DefaultResourceLoader is created and Reload'd.
	ResourceLoader ResourceLoader

	// SessionManager. Default: SessionManager.Create(cwd).
	SessionManager *SessionManager

	// SettingsManager. Default: SettingsManager.Create(cwd, agentDir).
	SettingsManager *SettingsManager

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
		authStorage = NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	}

	modelRegistry := opts.ModelRegistry
	if modelRegistry == nil {
		modelRegistry = NewModelRegistry(authStorage, DefaultModelsJsonPath(agentDir))
	}

	// Settings
	settingsManager := opts.SettingsManager
	if settingsManager == nil {
		settingsManager = NewSettingsManager(cwd, agentDir)
	}

	// Session
	sessionManager := opts.SessionManager
	if sessionManager == nil {
		sessionManager = NewSessionManager(cwd, DefaultSessionDir(agentDir, cwd))
	}

	// Resources
	resourceLoader := opts.ResourceLoader
	if resourceLoader == nil {
		rl := NewResourceLoader(ResourceLoaderOptions{
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
		result := FindInitialModel(FindInitialModelOptions{
			ScopedModels:         opts.ScopedModels,
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
				tl = string(DefaultThinkingLevel)
			}
			thinkingLevel = tl
		}
	}
	if thinkingLevel == "" {
		tl := settingsManager.GetDefaultThinkingLevel()
		if tl == "" {
			tl = string(DefaultThinkingLevel)
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
			Tools:         agentTools,
		},
		ConvertToLLM: func(messages []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(messages)
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
		agentOpts.ServerTools = resolveServerTools(serverToolNames)
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
		ScopedModels:     opts.ScopedModels,
	})

	return &CreateAgentSessionResult{
		Session:              session,
		ModelFallbackMessage: modelFallbackMessage,
	}, nil
}

// ============================================================================
// Helpers
// ============================================================================

// DefaultAgentDir returns the default global config directory (~/.fir/agent).
func DefaultAgentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".fir", "agent")
}

// PiAgentDir returns the pi agent directory (~/.pi/agent) for picking up
// sessions created by pi (Claude Code).
func PiAgentDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
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
var serverToolTypeMap = map[string]string{
	"web_search":               "web_search_20250305",
	"code_execution":           "code_execution_20250522",
	"programmatic_tool_calling": "programmatic_tool_calling_20250624",
}

// resolveServerTools converts short tool names (from settings) to AnthropicServerTool structs.
func resolveServerTools(names []string) []ai.AnthropicServerTool {
	var tools []ai.AnthropicServerTool
	for _, name := range names {
		toolType, ok := serverToolTypeMap[name]
		if !ok {
			// Allow raw type identifiers too (e.g. "web_search_20250305").
			toolType = name
		}
		tools = append(tools, ai.AnthropicServerTool{Type: toolType})
	}
	return tools
}
