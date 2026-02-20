// Ported from: packages/coding-agent/src/main.ts
// Upstream hash: 4ba3e5be
package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/ai/providers"
	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/core/compaction"
	"github.com/kfet/tau/pkg/core/tools"
	"github.com/kfet/tau/pkg/extension"
	interactive "github.com/kfet/tau/pkg/modes/interactive"
	printmode "github.com/kfet/tau/pkg/modes/print"
	rpcmode "github.com/kfet/tau/pkg/modes/rpc"

	// Import built-in extensions (registered via init())
	_ "github.com/kfet/tau/pkg/extensions/claudeusage"
	_ "github.com/kfet/tau/pkg/extensions/notify"
	_ "github.com/kfet/tau/pkg/extensions/sandbox"
	_ "github.com/kfet/tau/pkg/extensions/tmuxspinner"
)

//go:embed CHANGELOG.md
var changelogContent string

// sessionSetup holds common setup results shared between run modes.
type sessionSetup struct {
	cwd             string
	agentDir        string
	result          *core.CreateAgentSessionResult
	settingsManager *core.SettingsManager
	extSetup        *extension.SetupResult
}

// setupSession performs the initialization shared by all run modes:
// working directory, auth, model resolution, session creation, and extensions.
//
// When skipScopedOnContinue is true (print/RPC modes), the scoped-model
// default is skipped on --continue/--resume so the continued session keeps
// its original model.
func setupSession(args *Args, skipScopedOnContinue bool) (*sessionSetup, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("TAU_AGENT_DIR"); dir != "" {
		agentDir = dir
	}

	// Auth and model registry
	authStorage := core.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := core.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

	if args.ApiKey != "" {
		if args.Provider == "" {
			return nil, fmt.Errorf("--api-key requires --provider to be specified")
		}
		authStorage.SetRuntimeApiKey(args.Provider, args.ApiKey)
	}

	settingsManager := core.NewSettingsManager(cwd, agentDir)
	reportSettingsErrors(settingsManager, "startup")
	sessionManager := createSessionManager(args, cwd, agentDir)

	// Resource loader
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
		return nil, fmt.Errorf("reload resources: %w", err)
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
			return nil, fmt.Errorf("model %s/%s not found", args.Provider, args.Model)
		}
	} else if len(scopedModels) > 0 {
		if !skipScopedOnContinue || (!args.Continue && !args.Resume) {
			model = scopedModels[0].Model
		}
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
		CompactionRunner: &compaction.DefaultRunner{
			SettingsManager: settingsManager,
			ModelRegistry:   modelRegistry,
		},
	}

	if cliTools := resolveTools(args, cwd); cliTools != nil {
		sessionOpts.Tools = cliTools
	}

	if args.Thinking != "" {
		sessionOpts.ThinkingLevel = string(args.Thinking)
	}

	result, err := core.CreateAgentSession(context.Background(), sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Extensions
	extSetup, err := extension.Setup(result.Session, core.NewEventBus(), extension.SetupOptions{
		EnabledNames: resolveEnabledExtensions(args, settingsManager),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: extension setup failed: %v\n", err)
	}

	if extSetup != nil && extSetup.Runner != nil {
		for name := range extSetup.Runner.GetFlags() {
			if val, ok := args.UnknownFlags[name]; ok {
				extSetup.Runner.SetFlagValue(name, val)
			}
		}
	}

	// Warn about model fallback
	if result.ModelFallbackMessage != "" {
		fmt.Fprintln(os.Stderr, result.ModelFallbackMessage)
	}

	// Check model is available — in non-interactive modes we must fail early,
	// but in TUI mode we allow starting without a model so the user can /login.
	if err := checkModelAvailable(result.Session.Model(), args); err != nil {
		return nil, err
	}

	// Clamp thinking level to model capabilities
	clampThinkingLevel(result.Session, args.Thinking)

	return &sessionSetup{
		cwd:             cwd,
		agentDir:        agentDir,
		result:          result,
		settingsManager: settingsManager,
		extSetup:        extSetup,
	}, nil
}

// checkModelAvailable returns an error if no model is available and the mode
// requires one (print, JSON, RPC). Interactive TUI mode is allowed to start
// without a model so the user can /login.
func checkModelAvailable(model *ai.Model, args *Args) error {
	if model != nil {
		return nil
	}
	if args.Print || args.OutputMode == ModeJSON || args.OutputMode == ModeRPC {
		return fmt.Errorf("no models available\n\nSet an API key environment variable:\n  ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, etc.")
	}
	return nil
}

// thinkingLevelSetter is the interface needed by clampThinkingLevel.
type thinkingLevelSetter interface {
	Model() *ai.Model
	ThinkingLevel() string
	SetThinkingLevel(string)
}

// clampThinkingLevel adjusts the session's thinking level to match model
// capabilities. It is a no-op if thinking is empty or model is nil.
func clampThinkingLevel(s thinkingLevelSetter, thinking agent.ThinkingLevel) {
	if thinking == "" || s.Model() == nil {
		return
	}
	effective := string(thinking)
	if !s.Model().Reasoning {
		effective = "off"
	} else if effective == "xhigh" && !ai.SupportsXhigh(s.Model()) {
		effective = "high"
	}
	if effective != s.ThinkingLevel() {
		s.SetThinkingLevel(effective)
	}
}

// reportSettingsErrors reports any settings load errors to stderr.
func reportSettingsErrors(settingsManager *core.SettingsManager, context string) {
	for _, se := range settingsManager.DrainErrors() {
		fmt.Fprintf(os.Stderr, "Warning (%s, %s settings): %v\n", context, se.Scope, se.Err)
	}
}

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
		fmt.Println("tau " + version)
		return nil
	}

	if args.ListModels != nil {
		return runListModels(args)
	}

	// Read piped stdin (if not a TTY) — skip for RPC mode which reads stdin directly
	if args.OutputMode != ModeRPC {
		stdinContent := readPipedStdin()
		if stdinContent != "" {
			args.Print = true
			args.Messages = append([]string{stdinContent}, args.Messages...)
		}
	}

	// Determine mode
	isPrintMode := args.Print || args.OutputMode == ModeJSON
	isRPCMode := args.OutputMode == ModeRPC

	if !isPrintMode && !isRPCMode {
		return runInteractiveMode(args)
	}

	setup, err := setupSession(args, true)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()

	// Extension lifecycle for non-interactive modes
	if setup.extSetup != nil && setup.extSetup.Runner != nil {
		_ = setup.extSetup.Runner.EmitSessionStart()
		defer func() { _ = setup.extSetup.Runner.EmitSessionShutdown() }()
	}

	// Run RPC mode
	if isRPCMode {
		server := rpcmode.NewServer(setup.result.Session)
		return server.Run()
	}

	// Process @file arguments
	var initialMessage string
	var initialImages []ai.ImageContent
	var remainingMessages []string

	if len(args.FileArgs) > 0 {
		processed, err := ProcessFileArguments(args.FileArgs, setup.cwd)
		if err != nil {
			return err
		}
		if len(args.Messages) > 0 {
			initialMessage = processed.Text + args.Messages[0]
			remainingMessages = args.Messages[1:]
		} else {
			initialMessage = processed.Text
		}
		initialImages = processed.Images
	} else if len(args.Messages) > 0 {
		initialMessage = args.Messages[0]
		remainingMessages = args.Messages[1:]
	}

	// Run print mode
	outputMode := printmode.ModeText
	if args.OutputMode == ModeJSON {
		outputMode = printmode.ModeJSON
	}

	return printmode.Run(setup.result.Session, printmode.Options{
		Mode:           outputMode,
		InitialMessage: initialMessage,
		InitialImages:  initialImages,
		Messages:       remainingMessages,
	})
}

// runListModels lists available models and exits.
func runListModels(args *Args) error {
	agentDir := core.DefaultAgentDir()
	if dir := os.Getenv("TAU_AGENT_DIR"); dir != "" {
		agentDir = dir
	}

	authStorage := core.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := core.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

	if args.ApiKey != "" && args.Provider != "" {
		authStorage.SetRuntimeApiKey(args.Provider, args.ApiKey)
	}

	models := modelRegistry.GetAll()

	// Apply search pattern if provided
	pattern := ""
	if s, ok := args.ListModels.(string); ok {
		pattern = strings.ToLower(s)
	}

	for _, m := range models {
		if pattern != "" {
			name := strings.ToLower(m.Provider + "/" + m.ID)
			if !strings.Contains(name, pattern) {
				continue
			}
		}
		fmt.Printf("%s/%s\n", m.Provider, m.ID)
	}

	return nil
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

// resolveTools builds the tool list based on CLI --tools/--no-tools flags.
// Returns nil if no flags are set (use defaults).
func resolveTools(args *Args, cwd string) []agent.AgentTool {
	allToolMap := map[string]func(string) agent.AgentTool{
		"read":  tools.NewReadTool,
		"bash":  tools.NewBashTool,
		"edit":  tools.NewEditTool,
		"write": tools.NewWriteTool,
		"grep":  tools.NewGrepTool,
		"find":  tools.NewFindTool,
		"ls":    tools.NewLsTool,
	}

	if args.NoTools && len(args.Tools) == 0 {
		// --no-tools only: no tools at all
		return []agent.AgentTool{}
	}

	if len(args.Tools) > 0 {
		// --tools (with or without --no-tools): only include specified tools
		result := make([]agent.AgentTool, 0, len(args.Tools))
		for _, name := range args.Tools {
			if fn, ok := allToolMap[name]; ok {
				result = append(result, fn(cwd))
			} else {
				fmt.Fprintf(os.Stderr, "Warning: unknown tool %q (available: read, bash, edit, write, grep, find, ls)\n", name)
			}
		}
		return result
	}

	// No flags: use defaults (nil triggers defaults in SDK)
	return nil
}

// resolveEnabledExtensions computes the list of extension names to activate.
// Extensions are enabled via:
//   - settings.json "extensions" array (global or project)
//   - CLI --extension / -e flags
//
// --no-extensions disables all extensions regardless of config.
func resolveEnabledExtensions(args *Args, sm *core.SettingsManager) []string {
	if args.NoExtensions {
		return nil
	}

	seen := make(map[string]bool)
	var names []string

	// Add settings
	for _, n := range sm.GetEnabledExtensions() {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}

	// Add CLI --extension flags
	for _, n := range args.Extensions {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}

	return names
}

// runInteractiveMode runs the full interactive TUI mode.
func runInteractiveMode(args *Args) error {
	setup, err := setupSession(args, false)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()

	// Extension lifecycle: shutdown deferred, start happens after UI wiring
	if setup.extSetup != nil && setup.extSetup.Runner != nil {
		defer func() { _ = setup.extSetup.Runner.EmitSessionShutdown() }()
	}

	// Load keybindings
	keybindings := core.NewKeybindingsManager(setup.agentDir)

	// Process @file arguments and build initial prompt
	var initialPrompt string
	if len(args.FileArgs) > 0 {
		processed, err := ProcessFileArguments(args.FileArgs, setup.cwd)
		if err != nil {
			return err
		}
		initialPrompt = processed.Text
		if len(args.Messages) > 0 {
			initialPrompt += strings.Join(args.Messages, "\n")
		}
		// Note: images in interactive mode are not yet supported
	} else if len(args.Messages) > 0 {
		initialPrompt = strings.Join(args.Messages, "\n")
	}

	mode := interactive.NewInteractiveMode(
		setup.result.Session,
		keybindings,
		setup.settingsManager,
		interactive.InteractiveModeOptions{
			InitialPrompt:   initialPrompt,
			ThemeName:       "dark",
			ChangelogContent: changelogContent,
		},
	)

	// Wire extension setup into interactive mode (enables /reload for extensions).
	// This also sets the UIContext on the runner so that extensions can update
	// the footer status and show notifications.
	if setup.extSetup != nil {
		mode.SetExtensionSetup(setup.extSetup, args.Extensions)
		// Emit session_start after the UI context is wired so that extension
		// handlers (e.g. claude-usage) can call SetStatus successfully.
		if setup.extSetup.Runner != nil {
			_ = setup.extSetup.Runner.EmitSessionStart()
		}
	}

	if err := mode.Init(); err != nil {
		return fmt.Errorf("init interactive mode: %w", err)
	}

	return mode.Run(interactive.InteractiveModeOptions{
		InitialPrompt: initialPrompt,
	})
}
