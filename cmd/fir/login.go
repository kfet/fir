package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
	firpkg "github.com/kfet/fir/pkg/pkg"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/compaction"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/pinoauth"
)

// runLogin executes the interactive OAuth login flow for a given provider.
//
// It requires auth providers to be registered first (typically via extension
// loading). Unknown providers print an available-provider list and fail.
func runLogin(providerID string) error {
	provider := ai.GetOAuthProvider(providerID)
	if provider == nil {
		providers := ai.GetOAuthProviders()
		fmt.Fprintf(os.Stderr, "Unknown OAuth provider: %s\n\nAvailable providers:\n", providerID)
		for _, p := range providers {
			fmt.Fprintf(os.Stderr, "  %s - %s\n", p.ID(), p.Name())
		}
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))

	fmt.Fprintf(os.Stderr, "Logging in to %s...\n", provider.Name())

	callbacks := pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			openURL := session.PreferredAuthURL(info.URL, info.ShortURL)
			browserOpened := session.OpenBrowser(openURL) == nil
			formatted := session.FormatAuthURLs(info.URL, info.ShortURL)
			if browserOpened {
				fmt.Fprintf(os.Stderr, "Opening browser… if it doesn't appear:\n%s\n", formatted)
			} else {
				fmt.Fprintf(os.Stderr, "Open this URL to authenticate:\n%s\n", formatted)
			}
			if info.Instructions != "" {
				fmt.Fprintf(os.Stderr, "%s\n", info.Instructions)
			}
		},
		OnPrompt: func(prompt pinoauth.Prompt) (string, error) {
			fmt.Fprintf(os.Stderr, "%s ", prompt.Message)
			var input string
			_, err := fmt.Scanln(&input)
			return input, err
		},
		OnManualCodeInput: func() (string, error) {
			fmt.Fprintf(os.Stderr, "Paste the redirect URL or authorization code from your browser: ")
			var input string
			_, err := fmt.Scanln(&input)
			return input, err
		},
		OnDismissManualInput: func() {
			// Clear the manual-input prompt line (move to start, clear line).
			fmt.Fprintf(os.Stderr, "\r\033[K")
		},
		OnProgress: func(message string) {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		},
	}

	slot, label, err := authStorage.LoginAccount(context.Background(), providerID, callbacks)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if auth.IsSlotKey(slot) {
		who := label
		if who == "" {
			_, who = auth.SplitSlot(slot)
		}
		fmt.Fprintf(os.Stderr, "Added %s account %q. Credentials saved.\n", provider.Name(), who)
	} else if label != "" {
		fmt.Fprintf(os.Stderr, "Logged in to %s as %s. Credentials saved.\n", provider.Name(), label)
	} else {
		fmt.Fprintf(os.Stderr, "Logged in to %s. Credentials saved.\n", provider.Name())
	}
	return nil
}

// runLoginSubcommand implements the "fir login <provider>" subcommand.
//
// OAuth providers are contributed by auth extensions (e.g. anthropic_auth.py,
// copilot_auth.py), so this command starts a minimal session with extensions
// loaded before running the flow. Without extensions, only a handful of
// built-in providers would be visible.
//
// Usage:
//
//	fir login <provider-id>
//	fir login list
func runLoginSubcommand() error {
	subArgs := os.Args[2:] // skip "fir login"

	// Bedrock setup takes its own flag set (--mode/--profile/--token/…) which
	// the generic OAuth flag parser below would reject. Detect it up front and
	// hand the raw args to the Bedrock setup flow.
	for _, a := range subArgs {
		if isBedrockAlias(a) {
			return runBedrockSetup(subArgs)
		}
	}

	// Parse flags that control extension loading and debug. Mirrors the
	// main CLI semantics so users can narrow the auth extension set.
	// `-h`/`--help` is accepted at any position.
	args := &Args{}
	var positional []string
	for i := 0; i < len(subArgs); i++ {
		a := subArgs[i]
		switch {
		case a == "-h" || a == "--help":
			printLoginHelp()
			return nil
		case a == "--no-extensions":
			args.NoExtensions = true
		case (a == "--extension" || a == "-e") && i+1 < len(subArgs):
			i++
			args.Extensions = append(args.Extensions, subArgs[i])
		case (a == "--disable-extension" || a == "-d") && i+1 < len(subArgs):
			i++
			args.DisabledExtensions = append(args.DisabledExtensions, subArgs[i])
		case a == "--debug":
			args.Debug = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag for 'fir login': %s", a)
		default:
			positional = append(positional, a)
		}
	}

	// Subcommand dispatch happens before run()'s log init, so we own it here.
	debugEnabled := args.Debug || os.Getenv("FIR_DEBUG") == "1"
	debugPath := os.Getenv("FIR_DEBUG_LOG")
	if debugPath == "" {
		debugPath = filepath.Join(resolveAgentDir(), "debug.log")
	}
	logCleanup, err := firlog.Init(debugEnabled, debugPath, resolveDebugLogConfig())
	if err != nil {
		return fmt.Errorf("init debug log: %w", err)
	}
	defer logCleanup()

	// Register built-in API providers so the model registry is populated.
	// The --login flag path already does this in run() before reaching
	// runLoginWithExtensions; the subcommand path bypasses run(), so we
	// register here. RegisterDefaultProviders is idempotent (registry
	// entries are keyed by Api and simply overwritten).
	providers.RegisterDefaultProviders()

	// "fir login" or "fir login list": list available providers.
	if len(positional) == 0 || (len(positional) == 1 && positional[0] == "list") {
		return runLoginList(args)
	}
	if len(positional) > 1 {
		return fmt.Errorf("fir login: expected one provider id, got %d arguments", len(positional))
	}

	// Bedrock is not an OAuth provider — it's an interactive credential setup
	// (IAM profile/keys or bearer token). Intercept before the OAuth path.
	if isBedrockAlias(positional[0]) {
		return runBedrockSetup(subArgs)
	}

	return runLoginWithExtensions(args, positional[0])
}

// printLoginHelp prints the usage text for `fir login`.
func printLoginHelp() {
	fmt.Println("Usage: fir login <provider-id> [--no-extensions] [--extension name] [--disable-extension name] [--debug]")
	fmt.Println("       fir login bedrock [--mode iam-profile|iam-keys|bearer] [--account NAME] [--region REGION] ...")
	fmt.Println("       fir login list")
	fmt.Println()
	fmt.Println("Runs the OAuth login flow for the given provider and stores credentials.")
	fmt.Println("`fir login bedrock` configures an Amazon Bedrock account (IAM profile/keys or bearer).")
	fmt.Println("Logging in to the same provider again ADDS a second account; both are kept and switchable.")
	fmt.Println("Auth providers are contributed by extensions, which are loaded automatically.")
}

// runLoginList prints the available OAuth providers after loading extensions.
func runLoginList(args *Args) error {
	cleanup, err := startLoginSession(args)
	if err != nil {
		return err
	}
	defer cleanup()

	ps := ai.GetOAuthProviders()
	if len(ps) == 0 {
		fmt.Fprintln(os.Stderr, "No OAuth providers registered. Ensure auth extensions are installed and enabled.")
		return nil
	}
	fmt.Println("Available OAuth providers:")
	for _, p := range ps {
		fmt.Printf("  %s - %s\n", p.ID(), p.Name())
	}

	// Show currently stored accounts (per provider, including named slots).
	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	accts := authStorage.AllAccounts()
	if len(accts) > 0 {
		fmt.Println("\nStored accounts:")
		for _, a := range accts {
			tag := ""
			if a.AccountID == "" {
				tag = " (default)"
			}
			fmt.Printf("  %s  [%s]  %s%s\n", a.SlotKey, a.Type, a.DisplayName(), tag)
		}
		fmt.Println("\nRemove one with: fir logout <provider[#account]>")
	}
	return nil
}

// runLogoutSubcommand implements `fir logout <provider[#account]>`, removing a
// single stored account slot. With no argument it lists stored accounts.
func runLogoutSubcommand() error {
	args := os.Args[2:]
	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))

	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("Usage: fir logout <provider[#account]>")
		fmt.Println("       fir logout list")
		accts := authStorage.AllAccounts()
		if len(accts) > 0 {
			fmt.Println("\nStored accounts:")
			for _, a := range accts {
				fmt.Printf("  %s  [%s]  %s\n", a.SlotKey, a.Type, a.DisplayName())
			}
		}
		return nil
	}
	if args[0] == "list" {
		for _, a := range authStorage.AllAccounts() {
			fmt.Printf("  %s  [%s]  %s\n", a.SlotKey, a.Type, a.DisplayName())
		}
		return nil
	}
	slot := args[0]
	if !authStorage.Has(slot) {
		return fmt.Errorf("no stored account for slot %q", slot)
	}
	if err := authStorage.Logout(slot); err != nil {
		return fmt.Errorf("logout %s: %w", slot, err)
	}
	fmt.Fprintf(os.Stderr, "Removed account %s.\n", slot)
	return nil
}

// runLoginWithExtensions starts a throwaway session with extensions loaded,
// then runs the OAuth flow for providerID.
func runLoginWithExtensions(args *Args, providerID string) error {
	cleanup, err := startLoginSession(args)
	if err != nil {
		return err
	}
	defer cleanup()
	return runLogin(providerID)
}

// startLoginSession boots the minimum machinery needed for auth extensions to
// register OAuth providers: a lightweight agent session and the extension
// manager. Callers are responsible for initialising firlog and registering
// built-in API providers before calling. The returned cleanup function shuts
// extensions and the session down in reverse order.
func startLoginSession(args *Args) (func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	agentDir := resolveAgentDir()

	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	modelRegistry := models.NewModelRegistry(authStorage, filepath.Join(agentDir, "models.json"))
	settingsManager := config.NewSettingsManager(cwd, agentDir)
	reportSettingsErrors(settingsManager, "login")

	// Resource loader is needed so package-provided extensions are
	// discovered, matching the normal startup path.
	pkgMgr := firpkg.New(agentDir, cwd, settingsManager)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		SettingsManager: settingsManager,
		PackageResolver: pkgMgr,
		NoSkills:        true, // skills aren't needed for login
	})
	if err := rl.Reload(); err != nil {
		return nil, fmt.Errorf("reload resources: %w", err)
	}

	// Build a lightweight session — no MCP, ephemeral session store, no tools.
	opts := session.SetupOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		AuthStorage:     authStorage,
		ModelRegistry:   modelRegistry,
		SettingsManager: settingsManager,
		SessionStore:    store.InMemorySessionStore(),
		Tools:           []agent.AgentTool{},
		ResourceLoader:  rl,
		CompactionRunner: &compaction.DefaultRunner{
			SettingsManager: settingsManager,
			ModelRegistry:   modelRegistry,
		},
	}
	extReady := make(chan struct{})
	opts.ExtReady = extReady

	result, err := session.Setup(context.Background(), opts)
	if err != nil {
		return nil, fmt.Errorf("session setup: %w", err)
	}

	var extSetup *extension.SetupResult
	if !args.NoExtensions {
		extOpts := extension.SetupOptions{
			ProjectDir:          cwd,
			Cwd:                 cwd,
			Mode:                "login",
			Version:             version,
			EnabledNames:        resolveEnabledExtensions(args, settingsManager),
			DisabledNames:       args.DisabledExtensions,
			ExtraExtensionFiles: rl.GetPackageExtensionPaths(),
			ExtraExtensionDirs:  resources.ResolveSettingsExtensionPaths(cwd, settingsManager),
			ConfigDirs:          extensionConfigDirs(cwd),
		}
		extSetup, err = extension.Setup(result.Session, extOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: extension setup failed: %v\n", err)
		}
		if extSetup != nil {
			for _, f := range extSetup.StartFailures() {
				if f.IsAuth {
					fmt.Fprintf(os.Stderr, "Error: auth extension %q failed to start: %v\n", f.Name, f.Err)
				} else {
					fmt.Fprintf(os.Stderr, "Warning: extension %q failed to start: %v\n", f.Name, f.Err)
				}
			}
			extSetup.EmitSessionStart(nil)
		}
		// Refresh the session's model pointer so any auth-extension
		// ModifyModels hooks (e.g. OAuth Bearer headers) are applied.
		refreshSessionModel(result.Session, modelRegistry)
	}
	close(extReady)

	firlog.Debug("login session ready", "cwd", cwd, "extensions", extSetup != nil)

	cleanup := func() {
		if extSetup != nil {
			extSetup.EmitSessionShutdown()
		}
		result.Session.Close()
		if result.MCPManager != nil {
			_ = result.MCPManager.Close()
		}
	}
	return cleanup, nil
}
