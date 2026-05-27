// Ported from: packages/coding-agent/src/main.ts
// Upstream hash: 4ba3e5be
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	acpmode "github.com/kfet/fir/pkg/modes/acp"
	interactive "github.com/kfet/fir/pkg/modes/interactive"
	printmode "github.com/kfet/fir/pkg/modes/print"
	firpkg "github.com/kfet/fir/pkg/pkg"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/compaction"
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

	// extensionOpts is populated when DeferExtensions is true, so the
	// caller can run setupExtensions later (e.g. after the TUI renders).
	extensionOpts *extension.SetupOptions

	// ExtReady is closed once extensions have finished loading and the
	// session model has been refreshed with any auth headers (e.g. OAuth).
	// Code that needs a fully-initialised model before making API calls
	// (e.g. the initial prompt) should wait on this channel.
	ExtReady chan struct{}

	// mcpManager holds the MCP server manager (nil if no MCP servers configured).
	// The caller is responsible for calling Close() when the session ends.
	mcpManager *mcp.Manager
}

// setupSession performs the initialization shared by all run modes:
// working directory, auth, model resolution, session creation, and extensions.
func setupSession(args *Args, deferExtensions bool) (*sessionSetup, error) {
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

	// Open the session store early so we can restore the persisted
	// SessionInvocation (--mcp-config / --extension / --model / ...) before
	// any of the downstream args-derived setup runs. For brand-new sessions
	// this also stamps the current invocation into the header so future
	// `fir -c` invocations can restore the same config.
	sessionStore, isResumed := createSessionStore(args, cwd, agentDir)
	maybeRestoreInvocation(args, sessionStore, isResumed, os.Stderr)

	// Apply Claude Code-style env vars: CLAUDE_CODE_USE_BEDROCK=1 + ANTHROPIC_MODEL
	// route the model through Amazon Bedrock. ANTHROPIC_MODEL can be a model id
	// (e.g. "us.anthropic.claude-opus-4-6-v1") or a Bedrock ARN
	// (e.g. "arn:aws:bedrock:us-east-1:123:application-inference-profile/abc").
	// Explicit CLI flags take precedence over env vars.
	if os.Getenv("CLAUDE_CODE_USE_BEDROCK") == "1" {
		if envModel := os.Getenv("ANTHROPIC_MODEL"); envModel != "" {
			// Always register it so it shows up in the /model selector and
			// --list-models, even if the user has an explicit --model flag.
			registerBedrockEnvModel(modelRegistry, envModel)
			if args.Model == "" {
				args.Model = envModel
				if args.Provider == "" {
					args.Provider = "amazon-bedrock"
				}
			}
		}
	}

	// Model resolution from CLI flags is deferred until after extensions
	// register their providers/models. Built-in providers would resolve
	// fine here, but extension-shipped providers only become visible to
	// the registry after their auth extensions hand off ProviderSpec
	// records, so a single post-extensions resolution path (further
	// down in this function, after extension.Setup + refreshSessionModel)
	// avoids "Unknown provider" errors for ext-shipped IDs.
	var model *ai.Model

	// Usage tracking
	usageTracker := New(DefaultPath(agentDir))

	// Load MCP configs
	var mcpConfigs map[string]mcp.ServerConfig
	if !args.NoMCP {
		mcpCfg, mcpErr := mcp.LoadDefaultConfigs(cwd)
		if mcpErr == nil && args.MCPConfig != "" {
			extra, extraErr := mcp.LoadConfigFile(args.MCPConfig)
			if extraErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load --mcp-config %s: %v\n", args.MCPConfig, extraErr)
				mcpErr = extraErr
			} else {
				mcpCfg = mcp.MergeConfigs(mcpCfg, extra)
			}
		}
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load MCP config: %v\n", mcpErr)
		} else if len(mcpCfg.MCPServers) > 0 {
			mcpConfigs = mcpCfg.MCPServers
		}
	}

	// Resource loader
	pkgMgr := firpkg.New(agentDir, cwd, settingsManager)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:                  cwd,
		AgentDir:             agentDir,
		SettingsManager:      settingsManager,
		PackageResolver:      pkgMgr,
		SystemPrompt:         args.SystemPrompt,
		AppendSystemPrompt:   args.AppendSystemPrompt,
		NoSkills:             args.NoSkills,
		AdditionalSkillPaths: args.Skills,
	})
	if err := rl.Reload(); err != nil {
		return nil, fmt.Errorf("reload resources: %w", err)
	}

	// Build shared setup options
	opts := session.SetupOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		SettingsManager: settingsManager,
		SessionStore:    sessionStore,
		Model:           model,
		Tools:           resolveTools(args, cwd),
		ResourceLoader:  rl,
		UsageTracker:    NewSessionTracker(usageTracker),
		MCPConfigs:      mcpConfigs,
		CompactionRunner: &compaction.DefaultRunner{
			SettingsManager: settingsManager,
			ModelRegistry:   modelRegistry,
		},
	}
	if args.Thinking != "" {
		opts.ThinkingLevel = string(args.Thinking)
	}

	extReady := make(chan struct{})
	opts.ExtReady = extReady

	result, err := session.Setup(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	firlog.Debug("session created")

	if args.SessionName != "" {
		result.Session.SetSessionName(args.SessionName)
	}

	// Extensions — discover and start stdio-based extensions in .fir/extensions/
	var extSetup *extension.SetupResult
	var extOpts *extension.SetupOptions
	if !args.NoExtensions {
		extOpts = &extension.SetupOptions{
			ProjectDir:          cwd,
			Cwd:                 cwd,
			Mode:                resolveExtensionMode(args),
			Version:             version,
			EnabledNames:        resolveEnabledExtensions(args, settingsManager),
			DisabledNames:       args.DisabledExtensions,
			ExtraExtensionFiles: rl.GetPackageExtensionPaths(),
			ExtraExtensionDirs:  resolveSettingsExtensionPaths(cwd, settingsManager),
			ConfigDirs:          extensionConfigDirs(cwd),
		}
		if !deferExtensions {
			extSetup, err = extension.Setup(result.Session, *extOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: extension setup failed: %v\n", err)
			}
			// Report extension startup failures to stderr.
			if extSetup != nil {
				for _, f := range extSetup.StartFailures() {
					if f.IsAuth {
						fmt.Fprintf(os.Stderr, "Error: auth extension %q failed to start: %v\n", f.Name, f.Err)
					} else {
						fmt.Fprintf(os.Stderr, "Warning: extension %q failed to start: %v\n", f.Name, f.Err)
					}
				}
			}
			// Re-resolve the session's model from the registry so it
			// picks up any headers set by auth extension ModifyModels
			// hooks (e.g. OAuth Bearer token).  The debounced auto-
			// refresh updates the registry; we just need to swap the
			// session's stale pointer.
			refreshSessionModel(result.Session, modelRegistry)
		}
	}

	// Resolve model from CLI flags now that extensions have registered any
	// ext-shipped providers/models.
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
		result.Session.SetModel(model)
	}

	// Warn about model fallback
	if result.ModelFallbackMessage != "" {
		fmt.Fprintln(os.Stderr, result.ModelFallbackMessage)
	}

	// Check model is available
	if err := checkModelAvailable(result.Session.Model(), args); err != nil {
		return nil, err
	}

	clampThinkingLevel(result.Session, args.Thinking)
	recordCLIFlags(usageTracker, args)

	return &sessionSetup{
		cwd:      cwd,
		agentDir: agentDir,
		result: &session.CreateAgentSessionResult{
			Session:              result.Session,
			ModelFallbackMessage: result.ModelFallbackMessage,
		},
		settingsManager: settingsManager,
		extSetup:        extSetup,
		usageTracker:    usageTracker,
		resourceLoader:  rl,
		extensionOpts:   extOpts,
		mcpManager:      result.MCPManager,
		ExtReady:        extReady,
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
	available := session.AvailableThinkingLevelsForModel(s.Model())
	effective := string(agent.ClampThinkingLevel(thinking, available))
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

// mcpReloadFunc returns a callback that re-reads MCP configs from disk and
// applies the diff to the running manager.  When the manager is nil (no MCP
// servers were configured at startup) and configs are now present on disk, a
// new manager is created, wired to the session, and started.
func mcpReloadFunc(mgrPtr **mcp.Manager, sess *session.AgentSession, cwd string, args *Args) func() error {
	return func() error {
		return session.ReloadMCP(context.Background(), mgrPtr, sess, cwd, args.MCPConfig, nil)
	}
}

// waitMCPReady blocks until every MCP server has finished its initial
// connect/handshake (or until 30s elapses). Timeouts emit a stderr warning
// rather than aborting. Safe to call with a nil manager (no-op).
func waitMCPReady(mgr *mcp.Manager) {
	if mgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.WaitReady(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: timed out waiting for MCP servers to initialize: %v\n", err)
	}
}

// run is the main application logic.
func run() error {
	// Standalone subcommands — registry-driven (see subcommands.go).
	if len(os.Args) >= 2 {
		if h := dispatchSubcommand(os.Args[1]); h != nil {
			return h()
		}
		// Extension-registered CLI verbs (frontmatter `cli_verbs:`).
		// Reserved subcommands take precedence above; unknown verbs fall
		// through to ParseArgs as today. Skip lookup for flags — they
		// can never be a verb, and discovery walks the extension tree.
		first := os.Args[1]
		if first != "" && first[0] != '-' {
			if code, ok, err := tryRunExtensionVerb(first, os.Args[2:]); ok {
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				os.Exit(code)
			}
		}
	}

	// Register built-in API providers (Anthropic, OpenAI, Google, Bedrock)
	providers.RegisterDefaultProviders()

	// --agent-dir has already been stripped by main.applyAgentDirFlag
	// (which sets FIR_AGENT_DIR before subcommand dispatch), so it never
	// reaches ParseArgs here. The flag still has a parser case in args.go
	// for help/completion coverage; behaviour is driven entirely by the
	// process env that main set.
	args := ParseArgs(os.Args[1:])

	// Initialise debug logging (file-only, never stdout/stderr).
	// Determine effective log level from -v flags, --debug, FIR_LOG_LEVEL,
	// FIR_DEBUG (in that precedence). Explicit FIR_LOG_LEVEL wins over
	// FIR_DEBUG.
	level, enabled := resolveLogLevel(args)
	firlog.SetLevel(level)
	debugPath := args.DebugLogFile
	if debugPath == "" {
		debugPath = os.Getenv("FIR_DEBUG_LOG")
	}
	if debugPath == "" {
		debugPath = filepath.Join(resolveAgentDir(), "debug.log")
	}
	debugCleanup, err := firlog.Init(enabled, debugPath)
	if err != nil {
		return fmt.Errorf("init debug log: %w", err)
	}
	defer debugCleanup()

	if args.VerboseCount > 2 {
		firlog.Warn("verbose flag clamped",
			"requested", args.VerboseCount, "clamped", 2,
			"note", "-vv (Trace) is the maximum verbosity")
	}
	if args.LegacyVersionHint {
		fmt.Fprintln(os.Stderr,
			"note: -v now enables verbose logging. Use -V or --version to print the version.")
	}

	firlog.Info("fir starting", "version", version, "pid", os.Getpid(), "debugLog", debugPath, "logLevel", level.String())
	firlog.Debug("args parsed", "provider", args.Provider, "model", args.Model, "mode", args.OutputMode)

	if args.Help {
		PrintHelp()
		return nil
	}

	if args.Version {
		fmt.Println("fir " + version)
		url := licensesURL
		if url == "" {
			url = "https://github.com/kfet/fir-dist/releases"
		}
		fmt.Println("License: MIT — " + url)
		return nil
	}

	if args.ListModels != nil {
		return runListModels(args)
	}

	if args.ListAvailModels != nil {
		return runListAvailableModels(args)
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
		return runLoginWithExtensions(args, args.Login)
	}

	// Slash-skill invocation: `fir /<skill-name> <task...>` rewrites to a
	// directive message. Done after --help/--version/--list-models/--export
	// /--login so those exits are never blocked by an unknown-skill error.
	if err := rewriteSlashSkillMessages(args); err != nil {
		return err
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

	setup, err := setupSession(args, false)
	if err != nil {
		return err
	}
	close(setup.ExtReady) // extensions loaded synchronously
	defer setup.result.Session.Close()
	defer func() {
		if setup.mcpManager != nil {
			_ = setup.mcpManager.Close()
		}
	}()

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

	// Wait for MCP servers to finish their initial connection attempt so
	// any tools they provide are available before the first prompt.
	waitMCPReady(setup.mcpManager)

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

	rel, err := update.FetchLatest(ctx)
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

// runListAvailableModels lists models that are authenticated and confirmed
// available by the provider's live model list, then exits.
func runListAvailableModels(args *Args) error {
	if args.Debug {
		debugPath := args.DebugLogFile
		if debugPath == "" {
			debugPath = filepath.Join(resolveAgentDir(), "debug.log")
		}
		cleanup, _ := firlog.Init(true, debugPath)
		if cleanup != nil {
			defer cleanup()
		}
	}

	agentDir := resolveAgentDir()
	cacheDir := filepath.Join(agentDir, "cache")

	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))

	if args.ApiKey != "" && args.Provider != "" {
		authStorage.SetRuntimeApiKey(args.Provider, args.ApiKey)
	}

	// Kick off live model fetches and wait up to 10s for them to finish.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	modelRegistry.StartLiveModelFetch(ctx, cacheDir, nil)
	modelRegistry.WaitForLiveFetch(10 * time.Second)

	available := modelRegistry.GetAvailable()

	pattern := ""
	if s, ok := args.ListAvailModels.(string); ok {
		pattern = strings.ToLower(s)
	}

	for _, m := range available {
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
	setup, err := setupSession(args, false)
	if err != nil {
		return err
	}
	close(setup.ExtReady) // extensions loaded synchronously
	defer setup.result.Session.Close()

	path, err := setup.result.Session.ExportToHTML(args.Export)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	fmt.Println(path)
	return nil
}

// createSessionStore creates the appropriate session manager based on CLI args.
// Returns the store and whether the store reopened an existing session on
// disk (`true`) rather than creating a fresh one (`false`). The boolean is
// derived from the store's own `WasResumed()` — single source of truth —
// so `fir -c` / `--session <path>` correctly report "fresh" when no prior
// session file existed, allowing maybeRestoreInvocation to stamp instead of
// (vacuously) restore. In-memory sessions count as "not resumed".
func createSessionStore(args *Args, cwd, agentDir string) (*store.SessionStore, bool) {
	sessionDir := args.SessionDir
	if sessionDir == "" {
		sessionDir = store.DefaultSessionDir(agentDir, cwd)
	}

	if args.NoSession {
		return store.InMemorySessionStore(), false
	}
	if args.Session != "" {
		sm, forked := store.OpenSessionStore(filepath.Join(sessionDir, args.Session))
		if forked {
			fmt.Fprintln(os.Stderr, "fir: session is active in another window — branched with history preserved")
		}
		return sm, sm.WasResumed()
	}
	if args.Continue {
		sm, forked := store.ContinueRecentSession(cwd, sessionDir)
		if forked {
			fmt.Fprintln(os.Stderr, "fir: session is active in another window — branched with history preserved")
		}
		// ContinueRecentSession falls back to NewSessionStore when no
		// prior session exists; the store's WasResumed() correctly
		// reports false in that case so maybeRestoreInvocation will
		// stamp the current invocation instead of trying to restore.
		return sm, sm.WasResumed()
	}
	return store.NewSessionStore(cwd, sessionDir), false
}

// resolveAgentDir returns the agent directory, honouring FIR_AGENT_DIR if set.
// main applies --agent-dir by setting FIR_AGENT_DIR before this helper runs.
func resolveAgentDir() string {
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		return dir
	}
	return session.DefaultAgentDir()
}

// extensionConfigDirs returns the priority-ordered list of directories
// extensions may use to read/write their per-extension config files.
// Highest priority first: project-local .fir overrides global agent dir.
func extensionConfigDirs(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, ".fir"),
		resolveAgentDir(),
	}
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

var allToolMap = map[string]func(string) agent.AgentTool{
	"read":  tools.NewReadTool,
	"bash":  tools.NewBashTool,
	"edit":  tools.NewEditTool,
	"write": tools.NewWriteTool,
	"grep":  tools.NewGrepTool,
	"find":  tools.NewFindTool,
}

// allToolNames returns the sorted list of built-in tool names.
func allToolNames() []string {
	names := make([]string, 0, len(allToolMap))
	for k := range allToolMap {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// resolveTools builds the tool list based on CLI --tools/--no-tools flags.
// Returns nil if no flags are set (use defaults).
func resolveTools(args *Args, cwd string) []agent.AgentTool {
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
				fmt.Fprintf(os.Stderr, "Warning: unknown tool %q (available: %s)\n", name, strings.Join(allToolNames(), ", "))
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
	// Note: SIGHUP is deliberately not trapped. It takes its default
	// action (terminate) so tmux/ssh-driven hangups (e.g. the
	// `tmux respawn-window -k` used by `make poe-deploy`) cleanly kill
	// fir instead of re-execing it and leaking MCP subprocess trees.
	// /reexec is triggered via the explicit command path, not SIGHUP.
	return acpmode.RunAcpMode(acpmode.Options{
		AdditionalSkillPaths: args.Skills,
		NoSkills:             args.NoSkills,
		NoExtensions:         args.NoExtensions,
		NoMCP:                args.NoMCP,
		MCPConfig:            args.MCPConfig,
		WaitMCP:              args.WaitMCP,
		EnabledExtensions:    args.Extensions,
		DisabledExtensions:   args.DisabledExtensions,
	})
}

// runInteractiveMode runs the full interactive TUI mode.
func runInteractiveMode(args *Args, noticeCh <-chan string) error {
	setup, err := setupSession(args, true)
	if err != nil {
		return err
	}
	defer setup.result.Session.Close()
	defer func() {
		if setup.mcpManager != nil {
			_ = setup.mcpManager.Close()
		}
	}()

	// Note: no SIGHUP handler is installed. SIGHUP takes its default
	// action (terminate). The /reexec command drives re-exec explicitly
	// via reexec.Exec() on its own path, not through signal handling.

	// Load keybindings
	keybindings := tui.NewKeybindingsManager(setup.agentDir, filepath.Join(setup.cwd, config.ConfigDirName))

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
	} else if len(args.Messages) > 0 {
		initialPrompt = strings.Join(args.Messages, "\n")
	}

	// Build theme search dirs
	var themeSearchDirs []string
	if !args.NoThemes {
		themeSearchDirs = []string{filepath.Join(setup.agentDir, "themes")}
		themeSearchDirs = append(themeSearchDirs, setup.settingsManager.GetThemePaths()...)
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

	// mode is the concrete type; used for extension-specific calls that are
	// not part of the tui.UI interface (SetExtensionSetup, ReexecExtData, etc.).
	mode := interactive.NewInteractiveMode(
		setup.result.Session,
		keybindings,
		setup.settingsManager,
		interactive.InteractiveModeOptions{
			InitialPrompt:   initialPrompt,
			ThemeName:       themeName,
			ThemeSearchDirs: themeSearchDirs,
			AgentDir:        setup.agentDir,
			MCPStatus:       mcp.StatusFunc(setup.mcpManager),
			MCPDetails:      mcp.DetailsFunc(setup.mcpManager),
			MCPReload:       mcpReloadFunc(&setup.mcpManager, setup.result.Session, setup.cwd, args),
		},
	)
	interactive.SetVersion(version)

	// Notify the TUI when MCP servers finish initializing.
	if setup.mcpManager != nil {
		setup.mcpManager.SetOnServerReady(func(name string, err error) {
			mode.NotifyMCPServerReady(name, err)
		})
		setup.mcpManager.SetOnServerConnecting(func(name string) {
			mode.NotifyMCPServerConnecting(name)
		})
		setup.mcpManager.SetOnServerDisconnected(func(name string, err error) {
			mode.NotifyMCPServerDisconnected(name, err)
		})
	}

	// ui is the stable interface contract used for all lifecycle calls.
	var ui tui.UI = mode

	// Wire the update notice channel so the TUI shows it at startup.
	ui.SetUpdateChannel(noticeCh)

	// --wait-mcp: block until all MCP servers have finished their initial
	// connect handshake before the TUI starts, so an initial prompt sees
	// every MCP tool.
	if args.WaitMCP {
		fmt.Fprintln(os.Stderr, "Waiting for MCP servers to initialize...")
		waitMCPReady(setup.mcpManager)
	}

	if err := ui.Init(); err != nil {
		return fmt.Errorf("init interactive mode: %w", err)
	}

	// Set up extensions AFTER TUI init, in a background goroutine so the
	// prompt appears immediately. Extensions register their tools async —
	// they'll be ready before the user finishes typing their first message.
	//
	// extDone is closed when the goroutine finishes; extResult holds the
	// SetupResult so the main goroutine can shut it down race-free.
	extDone := make(chan struct{})
	var extResult atomic.Pointer[extension.SetupResult]
	if !args.NoExtensions && setup.extensionOpts != nil {
		go func() {
			defer close(extDone)
			defer close(setup.ExtReady)
			es, extErr := extension.Setup(setup.result.Session, *setup.extensionOpts)
			if extErr != nil {
				firlog.Warn("extension setup failed", "err", extErr)
			}
			if es != nil {
				extResult.Store(es)
				mode.SetExtensionSetup(es)
				mode.SetBeforeExtensionReload(func() error {
					if es.Manager != nil {
						es.Manager.SetAllowedNames(resolveEnabledExtensions(args, setup.settingsManager))
					}
					return nil
				})
				refreshSessionModel(setup.result.Session, setup.result.Session.ModelRegistryRef())
				es.EmitSessionStart(mode.ReexecExtData())
				mode.NotifyExtensionFailures(es.StartFailures())
			}
		}()
	} else {
		close(extDone)
		close(setup.ExtReady)
	}

	err = ui.Run()
	// Wait for the extension goroutine to finish before shutting down,
	// so we don't race on the SetupResult pointer.
	<-extDone
	if es := extResult.Load(); es != nil {
		es.EmitSessionShutdown()
	}
	ui.Cleanup()
	ui.ReexecIfRequested() // never returns if /reexec was used
	return err
}

// refreshSessionModel ensures the model registry has applied any pending
// auth-extension ModifyModels hooks, then re-resolves the session's model
// pointer from the registry.
func refreshSessionModel(sess *session.AgentSession, registry *models.ModelRegistry) {
	registry.Refresh()
	if cur := sess.Model(); cur != nil {
		if updated := registry.Find(cur.Provider, cur.ID); updated != nil {
			sess.SetModel(updated)
		}
	}
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
	if len(args.DisabledExtensions) > 0 {
		record("--disable-extension")
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
	if args.ListAvailModels != nil {
		record("--list-available-models")
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

// registerBedrockEnvModel ensures a Bedrock model with the given id (which may
// be a Bedrock ARN, an inference-profile id, or a regular Bedrock model id) is
// present in the registry, so it shows up in `/model`, `--list-models`, and
// resolves cleanly without sibling-synthesis warnings. If a model with that id
// is already registered for amazon-bedrock the call is a no-op.
func registerBedrockEnvModel(reg *models.ModelRegistry, id string) {
	for _, m := range reg.GetAll() {
		if m.Provider == string(ai.ProviderAmazonBedrock) && m.ID == id {
			return
		}
	}

	// Clone settings from the bedrock default model so cost/context-window/
	// reasoning flags inherit sensible values.
	var base *ai.Model
	defaultID := models.DefaultModelPerProvider(ai.ProviderAmazonBedrock)
	for _, m := range reg.GetAll() {
		if m.Provider != string(ai.ProviderAmazonBedrock) {
			continue
		}
		if m.ID == defaultID {
			base = m
			break
		}
		if base == nil {
			base = m
		}
	}
	if base == nil {
		// No bedrock model registered at all — synthesise a minimal one.
		base = &ai.Model{
			Provider:      string(ai.ProviderAmazonBedrock),
			Api:           ai.ApiBedrockConverseStream,
			Reasoning:     true,
			Input:         []string{"text", "image"},
			ContextWindow: 200000,
			MaxTokens:     8192,
		}
	}

	clone := *base
	clone.ID = id
	if strings.HasPrefix(id, "arn:") {
		clone.Name = "Bedrock (custom ARN)"
	} else {
		clone.Name = id
	}
	reg.AddRuntimeModel(&clone)
}

// resolveSettingsExtensionPaths resolves extensionPaths entries from settings
// (merged global + project) against cwd, matching the semantics used for
// skills/themes path settings. Relative paths resolve against cwd,
// '~' expands to $HOME, and absolute paths pass through unchanged.
func resolveSettingsExtensionPaths(cwd string, sm *config.SettingsManager) []string {
	if sm == nil {
		return nil
	}
	raw := sm.GetExtensionPaths()
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, p := range raw {
		r := resources.ResolveResourcePath(cwd, p)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
