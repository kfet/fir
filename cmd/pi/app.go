// Ported from: packages/coding-agent/src/main.ts
// Upstream hash: 1caadb2e
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/ai/providers"
	"github.com/kfet/pi-go/pkg/core"
	printmode "github.com/kfet/pi-go/pkg/modes/print"
)

// run is the main application logic.
func run() error {
	// Register built-in API providers (Anthropic, OpenAI, Google, Bedrock)
	providers.RegisterDefaultProviders()

	args := ParseArgs(os.Args[1:], nil)

	if args.Help {
		PrintHelp()
		return nil
	}

	if args.Version {
		fmt.Println("pi-go " + version)
		return nil
	}

	// Read piped stdin (if not a TTY)
	stdinContent := readPipedStdin()
	if stdinContent != "" {
		args.Print = true
		args.Messages = append([]string{stdinContent}, args.Messages...)
	}

	// Determine mode
	isPrintMode := args.Print || args.OutputMode == ModeJSON

	if !isPrintMode {
		return fmt.Errorf("interactive mode not yet implemented. Use -p for print mode or pipe input")
	}

	// Resolve working directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("PI_AGENT_DIR"); dir != "" {
		agentDir = dir
	}

	// Create auth storage and model registry
	authStorage := core.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := core.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

	// Handle CLI --api-key as runtime override
	if args.ApiKey != "" {
		provider := args.Provider
		if provider == "" {
			return fmt.Errorf("--api-key requires --provider to be specified")
		}
		authStorage.SetRuntimeApiKey(provider, args.ApiKey)
	}

	// Create settings manager
	settingsManager := core.NewSettingsManager(cwd, agentDir)

	// Create session manager
	sessionManager := createSessionManager(args, cwd, agentDir)

	// Create resource loader
	rl := core.NewResourceLoader(core.ResourceLoaderOptions{
		Cwd:                           cwd,
		AgentDir:                      agentDir,
		SettingsManager:               settingsManager,
		SystemPrompt:                  args.SystemPrompt,
		AppendSystemPrompt:            args.AppendSystemPrompt,
		NoSkills:                      args.NoSkills,
		AdditionalSkillPaths:          args.Skills,
		AdditionalPromptTemplatePaths: args.PromptTemplates,
		NoPromptTemplates:             args.NoPromptTemplates,
	})
	if err := rl.Reload(); err != nil {
		return fmt.Errorf("reload resources: %w", err)
	}

	// Resolve scoped models
	var scopedModels []core.ScopedModel
	modelPatterns := args.Models
	if len(modelPatterns) == 0 {
		modelPatterns = settingsManager.GetEnabledModels()
	}
	if len(modelPatterns) > 0 {
		scopedModels = core.ResolveModelScope(modelPatterns, modelRegistry)
	}

	// Resolve model from CLI flags
	var model *ai.Model
	if args.Provider != "" && args.Model != "" {
		model = modelRegistry.Find(args.Provider, args.Model)
		if model == nil {
			return fmt.Errorf("model %s/%s not found", args.Provider, args.Model)
		}
	} else if len(scopedModels) > 0 && !args.Continue && !args.Resume {
		model = scopedModels[0].Model
	}

	// Build session options
	sessionOpts := core.CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		Model:           model,
		SessionManager:  sessionManager,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ScopedModels:    scopedModels,
	}

	if args.Thinking != "" {
		sessionOpts.ThinkingLevel = string(args.Thinking)
	}

	// Create the agent session
	result, err := core.CreateAgentSession(context.Background(), sessionOpts)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer result.Session.Close()

	// Warn about model fallback
	if result.ModelFallbackMessage != "" {
		fmt.Fprintln(os.Stderr, result.ModelFallbackMessage)
	}

	// Check model is available
	if result.Session.Model() == nil {
		fmt.Fprintln(os.Stderr, "No models available.")
		fmt.Fprintln(os.Stderr, "\nSet an API key environment variable:")
		fmt.Fprintln(os.Stderr, "  ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, etc.")
		os.Exit(1)
	}

	// Clamp thinking level to model capabilities
	if result.Session.Model() != nil && args.Thinking != "" {
		effectiveThinking := string(args.Thinking)
		if !result.Session.Model().Reasoning {
			effectiveThinking = "off"
		} else if effectiveThinking == "xhigh" && !ai.SupportsXhigh(result.Session.Model()) {
			effectiveThinking = "high"
		}
		if effectiveThinking != result.Session.ThinkingLevel() {
			result.Session.SetThinkingLevel(effectiveThinking)
		}
	}

	// Determine initial message and remaining messages
	var initialMessage string
	var remainingMessages []string
	if len(args.Messages) > 0 {
		initialMessage = args.Messages[0]
		remainingMessages = args.Messages[1:]
	}

	// Run print mode
	outputMode := printmode.ModeText
	if args.OutputMode == ModeJSON {
		outputMode = printmode.ModeJSON
	}

	return printmode.Run(result.Session, printmode.Options{
		Mode:           outputMode,
		InitialMessage: initialMessage,
		Messages:       remainingMessages,
	})
}

// createSessionManager creates the appropriate session manager based on CLI args.
func createSessionManager(args *Args, cwd, agentDir string) *core.SessionManager {
	sessionDir := args.SessionDir
	if sessionDir == "" {
		sessionDir = core.DefaultSessionDir(agentDir, cwd)
	}

	if args.NoSession {
		return core.InMemorySessionManager()
	}
	if args.Session != "" {
		return core.OpenSessionManager(filepath.Join(sessionDir, args.Session))
	}
	if args.Continue {
		return core.ContinueRecentSession(cwd, sessionDir)
	}
	return core.NewSessionManager(cwd, sessionDir)
}

// readPipedStdin reads all content from piped stdin.
// Returns empty string if stdin is a TTY.
func readPipedStdin() string {
	info, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}
	// Check if stdin is a pipe/redirect (not a terminal)
	if (info.Mode() & os.ModeCharDevice) != 0 {
		return ""
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(scanner.Text())
	}
	return strings.TrimSpace(sb.String())
}
