// Ported from: packages/coding-agent/src/main.ts
// Upstream hash: 4ba3e5be
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/compaction"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
	acpmode "github.com/kfet/fir/pkg/modes/acp"
	interactive "github.com/kfet/fir/pkg/modes/interactive"
	printmode "github.com/kfet/fir/pkg/modes/print"
	firpkg "github.com/kfet/fir/pkg/pkg"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/tui"
	"github.com/kfet/fir/pkg/update"
)

// sessionSetup holds common setup results shared between run modes.
type sessionSetup struct {
	cwd             string
	agentDir        string
	result          *session.CreateAgentSessionResult
	settingsManager *config.SettingsManager
	extSetup        *extension.SetupResult
	usageTracker    *Tracker
	resourceLoader  resources.ResourceLoader
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

	agentDir := resolveAgentDir()

	// Auth and model registry
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

	if args.ApiKey != "" {
		if args.Provider == "" {
			return nil, fmt.Errorf("--api-key requires --provider to be specified")
		}
		authStorage.SetRuntimeApiKey(args.Provider, args.ApiKey)
	}

	settingsManager := config.NewSettingsManager(cwd, agentDir)
	firlog.Debug("settings loaded", "cwd", cwd, "agentDir", agentDir)
	reportSettingsErrors(settingsManager, "startup")
	sessionManager := createSessionManager(args, cwd, agentDir)

	// Resource loader
	pkgMgr := firpkg.New(agentDir, cwd, settingsManager)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:                           cwd,
		AgentDir:                      agentDir,
		SettingsManager:               settingsManager,
		PackageResolver:               pkgMgr,
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
	var scopedModels []models.ScopedModel
	modelPatterns := args.Models
	if len(modelPatterns) == 0 {
		modelPatterns = settingsManager.GetEnabledModels()
	}
	if len(modelPatterns) > 0 {
		scopedModels = models.ResolveModelScope(modelPatterns, modelRegistry)
	}

	// Resolve model from CLI flags
	var model *ai.Model
	if args.Model != "" {
		resolved := models.ResolveCliModel(models.ResolveCliModelOptions{
			CLIProvider:   args.Provider,
			CLIModel:      args.Model,
			ModelRegistry: modelRegistry,
		})
		if resolved.Warning != "" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", resolved.Warning)
		}
		if resolved.Error != "" {
			return nil, fmt.Errorf("%s", resolved.Error)
		}
		model = resolved.Model
		firlog.Info("model resolved", "provider", model.Provider, "model", model.ID, "source", "cli")
		// "--model <pattern>:<thinking>" shorthand; explicit --thinking takes precedence.
		if args.Thinking == "" && resolved.ThinkingLevel != "" {
			args.Thinking = agent.ThinkingLevel(resolved.ThinkingLevel)
		}
	} else if len(scopedModels) > 0 {
		if !skipScopedOnContinue || (!args.Continue && !args.Resume) {
			model = scopedModels[0].Model
			firlog.Info("model resolved", "provider", model.Provider, "model", model.ID, "source", "scoped")
		}
	}

	// Usage tracking
	usageTracker := New(DefaultPath(agentDir))

	// Build session options
	sessionOpts := session.CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		Model:           model,
		SessionManager:  sessionManager,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ScopedModels:    scopedModels,
		UsageTracker:    NewSessionTracker(usageTracker),
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

	result, err := session.CreateAgentSession(context.Background(), sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	firlog.Debug("session created")

	// Extensions — discover and start stdio-based extensions in .fir/extensions/
	var extSetup *extension.SetupResult
	if !args.NoExtensions {
		extSetup, err = extension.Setup(result.Session, extension.SetupOptions{
			ProjectDir:          cwd,
			Cwd:                 cwd,
			Mode:                resolveExtensionMode(args),
			EnabledNames:        resolveEnabledExtensions(args, settingsManager),
			ExtraExtensionFiles: rl.GetPackageExtensionPaths(),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: extension setup failed: %v\n", err)
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

	// Record CLI usage
	recordCLIFlags(usageTracker, args)

	return &sessionSetup{
		cwd:             cwd,
		agentDir:        agentDir,
		result:          result,
		settingsManager: settingsManager,
		extSetup:        extSetup,
		usageTracker:    usageTracker,
		resourceLoader:  rl,
	}, nil
}

func resolveExtensionMode(args *Args) string {
	if args.OutputMode == ModeJSON {
		return "json"
	}
	if args.OutputMode == ModeACP {
		return "acp"
	}
	if args.Print {
		return "text"
	}
	return "interactive"
}

// checkModelAvailable returns an error if no model is available and the mode
// requires one (print, JSON). Interactive TUI mode is allowed to start
// without a model so the user can /login.
func checkModelAvailable(model *ai.Model, args *Args) error {
	if model != nil {
		return nil
	}
	if args.Print || args.OutputMode == ModeJSON {
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
		firlog.Debug("thinking level", "requested", string(thinking), "clamped", effective)
		s.SetThinkingLevel(effective)
	}
}

// reportSettingsErrors reports any settings load errors to stderr.
func reportSettingsErrors(settingsManager *config.SettingsManager, context string) {
	for _, se := range settingsManager.DrainErrors() {
		fmt.Fprintf(os.Stderr, "Warning (%s, %s settings): %v\n", context, se.Scope, se.Err)
	}
}

// run is the main application logic.
func run() error {
	// Standalone subcommands — handle before normal parsing.
	if len(os.Args) >= 2 && os.Args[1] == "update" {
		return runUpdate()
	}
	if len(os.Args) >= 2 && os.Args[1] == "skills" {
		return runSkills()
	}
	if len(os.Args) >= 2 && os.Args[1] == "extensions" {
		return runExtensions()
	}
	if len(os.Args) >= 2 && os.Args[1] == "install" {
		return runInstall()
	}
	if len(os.Args) >= 2 && os.Args[1] == "uninstall" {
		return runUninstall()
	}
	if len(os.Args) >= 2 && os.Args[1] == "packages" {
		return runPackages()
	}
	if len(os.Args) >= 2 && os.Args[1] == "pty" {
		runPTY()
		return nil
	}

	// Register built-in API providers (Anthropic, OpenAI, Google, Bedrock)
	providers.RegisterDefaultProviders()

	args := ParseArgs(os.Args[1:])

	// Initialise debug logging (file-only, never stdout/stderr).
	debugEnabled := args.Debug || os.Getenv("FIR_DEBUG") == "1"
	debugPath := args.DebugLogFile
	if debugPath == "" {
		debugPath = os.Getenv("FIR_DEBUG_LOG")
	}
	if debugPath == "" {
		debugPath = filepath.Join(resolveAgentDir(), "debug.log")
	}
	debugCleanup, err := firlog.Init(debugEnabled, debugPath)
	if err != nil {
		return fmt.Errorf("init debug log: %w", err)
	}
	defer debugCleanup()

	firlog.Info("fir starting", "version", version, "pid", os.Getpid(), "debugLog", debugPath)
	firlog.Debug("args parsed", "provider", args.Provider, "model", args.Model, "mode", args.OutputMode)

	if args.Help {
		PrintHelp()
		return nil
	}

	if args.Version {
		fmt.Println("fir " + version)
		return nil
	}

	if args.ListModels != nil {
		return runListModels(args)
	}

	if args.Export != "" {
		return runExport(args)
	}

	// Resolve agentDir early so the version check can use the cache.
	agentDir := resolveAgentDir()

	// Start async version check for interactive and print modes.
	// Skipped for machine-to-machine modes (ACP).
	// The channel always receives exactly one value (notice text or "").
	noticeCh := make(chan string, 1)
	wantUpdateCheck := args.OutputMode != ModeACP
	if wantUpdateCheck {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rel, _ := update.CheckLatest(ctx, version, agentDir)
			if rel != nil {
				noticeCh <- update.UpdateNotice(rel.Version)
			} else {
				noticeCh <- ""
			}
		}()
	} else {
		noticeCh <- ""
	}

	// printUpdateNotice prints the update notice to stderr (non-blocking).
	// Used for print mode — interactive mode shows it inside the TUI instead.
	printUpdateNotice := func() {
		drainUpdateNotice(noticeCh)
	}

	// Read piped stdin (if not a TTY) — skip for ACP mode which reads stdin directly
	if args.OutputMode != ModeACP {
		stdinContent := readPipedStdin()
		if stdinContent != "" {
			args.Print = true
			args.Messages = append([]string{stdinContent}, args.Messages...)
		}
	}

	// Determine mode
	isPrintMode := args.Print || args.OutputMode == ModeJSON
	isACPMode := args.OutputMode == ModeACP

	// Handle --login: run interactive OAuth login and exit.
	if args.Login != "" {
		return runLogin(args)
	}

	// ACP mode creates sessions on demand, so dispatch before setupSession.
	if isACPMode {
		firlog.Debug("mode dispatch", "mode", "acp")
		return runAcpMode(args)
	}

	if !isPrintMode {
		firlog.Debug("mode dispatch", "mode", "interactive")
		return runInteractiveMode(args, noticeCh)
	}

	setup, err := setupSession(args, true)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()

	// Extension lifecycle for non-interactive modes
	if setup.extSetup != nil {
		setup.extSetup.EmitSessionStart(nil)
		defer func() { setup.extSetup.EmitSessionShutdown() }()
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

	runErr := printmode.Run(setup.result.Session, printmode.Options{
		Mode:           outputMode,
		InitialMessage: initialMessage,
		InitialImages:  initialImages,
		Messages:       remainingMessages,
	})
	printUpdateNotice()
	return runErr
}

// runUpdate implements the "fir update" subcommand.
// Downloads and replaces the running binary from the latest GitHub release.
func runUpdate() error {

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Fprintln(os.Stderr, "Checking for updates...")

	rel, err := update.FetchLatestOrGH(ctx)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}

	if !update.IsNewer(rel.Version, version) {
		fmt.Fprintf(os.Stderr, "fir %s is already up to date.\n", version)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Updating fir %s → %s...\n", version, rel.Version)
	if err := update.SelfUpdate(ctx, rel); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Successfully updated to fir %s.\n", rel.Version)
	return nil
}

// runListModels lists available models and exits.
func runListModels(args *Args) error {
	agentDir := resolveAgentDir()

	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

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

// runExport exports a session to HTML and exits.
// --session is required so there is an existing session to export.
func runExport(args *Args) error {
	if args.Session == "" {
		return fmt.Errorf("--export requires --session <id> to identify the session to export")
	}
	setup, err := setupSession(args, true)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()

	path, err := setup.result.Session.ExportToHTML(args.Export)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	fmt.Println(path)
	return nil
}

// createSessionManager creates the appropriate session manager based on CLI args.
func createSessionManager(args *Args, cwd, agentDir string) *store.SessionManager {
	sessionDir := args.SessionDir
	if sessionDir == "" {
		sessionDir = store.DefaultSessionDir(agentDir, cwd)
	}

	if args.NoSession {
		return store.InMemorySessionManager()
	}
	if args.Session != "" {
		return store.OpenSessionManager(filepath.Join(sessionDir, args.Session))
	}
	if args.Continue {
		return store.ContinueRecentSession(cwd, sessionDir)
	}
	return store.NewSessionManager(cwd, sessionDir)
}

// resolveAgentDir returns the agent directory, honouring FIR_AGENT_DIR if set.
func resolveAgentDir() string {
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		return dir
	}
	return session.DefaultAgentDir()
}

// drainUpdateNotice non-blockingly reads a notice from noticeCh and prints
// it to stderr if non-empty. Used by print mode after the run completes.
func drainUpdateNotice(noticeCh <-chan string) {
	select {
	case notice := <-noticeCh:
		if notice != "" {
			fmt.Fprintln(os.Stderr, notice)
		}
	default:
		// Check still in flight — skip rather than block.
	}
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

// resolveEnabledExtensions computes the allowlist of extension names to activate.
// Extensions are enabled via:
//   - settings.json "extensions" array (global or project)
//   - CLI --extension / -e flags
//
// When the combined list is empty, all discovered extensions are started.
// --no-extensions disables all extensions regardless of config (handled by the caller).
func resolveEnabledExtensions(args *Args, sm *config.SettingsManager) []string {
	seen := make(map[string]bool)
	var names []string

	for _, n := range sm.GetEnabledExtensions() {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	for _, n := range args.Extensions {
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	return names
}

// runAcpMode runs ACP mode over stdin/stdout.
func runAcpMode(args *Args) error {
	acpmode.SetVersion(version)
	return acpmode.RunAcpMode(acpmode.Options{
		AdditionalSkillPaths:          args.Skills,
		AdditionalPromptTemplatePaths: args.PromptTemplates,
		NoSkills:                      args.NoSkills,
		NoPromptTemplates:             args.NoPromptTemplates,
		NoExtensions:                  args.NoExtensions,
		EnabledExtensions:             args.Extensions,
	})
}

// runInteractiveMode runs the full interactive TUI mode.
func runInteractiveMode(args *Args, noticeCh <-chan string) error {
	setup, err := setupSession(args, false)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()

	// Extension lifecycle: shutdown deferred, start happens after UI wiring
	if setup.extSetup != nil {
		defer func() { setup.extSetup.EmitSessionShutdown() }()
	}

	// Load keybindings
	keybindings := tui.NewKeybindingsManager(setup.agentDir)

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

	// Build theme search dirs: the user's global themes folder, any paths
	// from project/global settings.json, and any paths supplied via
	// --theme flags. A --theme flag may point to a .json file (use its
	// parent dir) or directly to a directory.
	// --no-themes disables all discovery; only the built-in dark/light themes remain.
	var themeSearchDirs []string
	if !args.NoThemes {
		themeSearchDirs = []string{filepath.Join(setup.agentDir, "themes")}
		themeSearchDirs = append(themeSearchDirs, setup.settingsManager.GetThemePaths()...)
		// Add directories containing package-contributed theme files.
		if setup.resourceLoader != nil {
			for _, p := range setup.resourceLoader.GetPackageThemePaths() {
				themeSearchDirs = append(themeSearchDirs, filepath.Dir(p))
			}
		}
		for _, p := range args.Themes {
			info, err := os.Stat(p)
			if err == nil && info.IsDir() {
				themeSearchDirs = append(themeSearchDirs, p)
			} else if err == nil && strings.HasSuffix(p, ".json") {
				themeSearchDirs = append(themeSearchDirs, filepath.Dir(p))
			}
		}
	}

	themeName := setup.settingsManager.GetTheme()

	mode := interactive.NewInteractiveMode(
		setup.result.Session,
		keybindings,
		setup.settingsManager,
		interactive.InteractiveModeOptions{
			InitialPrompt:   initialPrompt,
			ThemeName:       themeName,
			ThemeSearchDirs: themeSearchDirs,
		},
	)
	interactive.SetVersion(version)

	// Wire extension setup into interactive mode (enables /reload for extensions).
	if setup.extSetup != nil {
		mode.SetExtensionSetup(setup.extSetup)
		mode.SetBeforeExtensionReload(func() error {
			if setup.extSetup.Manager != nil {
				setup.extSetup.Manager.SetAllowedNames(resolveEnabledExtensions(args, setup.settingsManager))
			}
			return nil
		})
	}

	// Wire the update notice channel so the TUI shows it at startup.
	mode.SetUpdateChannel(noticeCh)

	if err := mode.Init(); err != nil {
		return fmt.Errorf("init interactive mode: %w", err)
	}

	// Emit session_start after Init() so that:
	//   1. Extension status callbacks are active (UI is wired).
	//   2. preloadReexecSidecar has run and ReexecExtData() is populated.
	if setup.extSetup != nil {
		setup.extSetup.EmitSessionStart(mode.ReexecExtData())
	}

	err = mode.Run(interactive.InteractiveModeOptions{
		InitialPrompt: initialPrompt,
	})
	mode.ReexecIfRequested() // never returns if /reexec was used
	return err
}

// recordCLIFlags records which CLI flags were used for usage tracking.
func recordCLIFlags(tracker *Tracker, args *Args) {
	record := func(flag string) { tracker.Record(EventCLIFlag, flag) }

	if args.Provider != "" {
		record("--provider")
	}
	if args.Model != "" {
		record("--model")
	}
	if args.ApiKey != "" {
		record("--api-key")
	}
	if args.SystemPrompt != "" {
		record("--system-prompt")
	}
	if args.AppendSystemPrompt != "" {
		record("--append-system-prompt")
	}
	if args.Thinking != "" {
		record("--thinking")
	}
	if args.Continue {
		record("--continue")
	}
	if args.Resume {
		record("--resume")
	}
	if args.Print {
		record("--print")
	}
	if args.NoSession {
		record("--no-session")
	}
	if args.Session != "" {
		record("--session")
	}
	if len(args.Models) > 0 {
		record("--models")
	}
	if args.NoTools {
		record("--no-tools")
	}
	if len(args.Tools) > 0 {
		record("--tools")
	}
	if args.NoExtensions {
		record("--no-extensions")
	}
	if len(args.Extensions) > 0 {
		record("--extension")
	}
	if args.NoSkills {
		record("--no-skills")
	}
	if len(args.Skills) > 0 {
		record("--skill")
	}
	if args.Export != "" {
		record("--export")
	}
	if args.Verbose {
		record("--verbose")
	}
	if args.Debug {
		record("--debug")
	}
	if args.ListModels != nil {
		record("--list-models")
	}
	if len(args.FileArgs) > 0 {
		record("@file")
	}

	// Record the mode
	switch args.OutputMode {
	case ModeJSON:
		tracker.Record(EventMode, "json")
	case ModeACP:
		tracker.Record(EventMode, "acp")
	default:
		if args.Print {
			tracker.Record(EventMode, "print")
		} else {
			tracker.Record(EventMode, "interactive")
		}
	}

	// Record session type
	if args.Continue {
		tracker.Record(EventSession, "continue")
	} else if args.Resume {
		tracker.Record(EventSession, "resume")
	} else {
		tracker.Record(EventSession, "new")
	}
}
