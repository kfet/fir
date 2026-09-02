package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/auth"
	firlog "github.com/kfet/fir/pkg/log"
)

// defaultAuthProvider is the provider `fir auth refresh` acts on when none is
// named. It is a default, not a hardcoding: every provider-specific detail
// (token endpoint, client id, body encoding, expiry normalisation) comes from
// the extension's declare_oauth_provider spec and its auth_post_exchange hook.
const defaultAuthProvider = "anthropic"

// authRefreshTimeout bounds the whole refresh run. A cron invocation must not
// hang forever on a wedged token endpoint.
const authRefreshTimeout = 2 * time.Minute

// runAuthSubcommand implements `fir auth <verb>`.
//
// Today the only verb is `refresh`, whose job is to keep a stored OAuth
// credential alive on an idle box with zero inference: fir's normal refresh
// only happens as a side effect of running a turn, so a machine that sits idle
// eventually lets its refresh token expire and needs an interactive browser
// re-login. This gives cron something cheap and honest to call instead.
func runAuthSubcommand() error {
	args := os.Args[2:] // skip "fir auth"

	if len(args) == 0 {
		printAuthHelp()
		return nil
	}
	switch args[0] {
	case "-h", "--help", "help":
		printAuthHelp()
		return nil
	case "refresh":
		return runAuthRefresh(args[1:])
	default:
		printAuthHelp()
		return fmt.Errorf("unknown 'fir auth' subcommand: %s", args[0])
	}
}

func printAuthHelp() {
	fmt.Println("Usage: fir auth refresh [provider-id] [--force] [--within DURATION]")
	fmt.Println()
	fmt.Println("Refresh stored OAuth credentials without running a turn — for cron on")
	fmt.Println("idle machines, where the refresh token would otherwise expire and")
	fmt.Println("force an interactive browser re-login.")
	fmt.Println()
	fmt.Println("Every account slot of the provider is refreshed, default account first.")
	fmt.Println("One slot failing does not stop the others; the exit status is non-zero")
	fmt.Println("if any slot failed.")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Printf("  --within DURATION   Refresh only if expiry is within this window (default %s)\n", auth.DefaultRefreshWindow)
	fmt.Println("  --force             Refresh regardless of how fresh the token is")
	fmt.Println("  --no-extensions, --extension NAME, --disable-extension NAME, --debug")
	fmt.Println()
	fmt.Printf("Provider defaults to %q. Output is one line per slot:\n", defaultAuthProvider)
	fmt.Println("  <slot>\t<label>\t<outcome>\texpires=<RFC3339>\tin=<duration>")
	fmt.Println()
	fmt.Println("Note: a refresh grant ROTATES the credential, and some providers (Anthropic)")
	fmt.Println("revoke the previous access token immediately. Copying the agent dir does not")
	fmt.Println("isolate a --force test — the refresh token in the copy is the same credential")
	fmt.Println("upstream. Use a separate login to test --force.")
}

// runAuthRefresh implements `fir auth refresh [provider-id]`.
func runAuthRefresh(subArgs []string) error {
	cliArgs := &Args{}
	var positional []string
	window := auth.DefaultRefreshWindow
	force := false

	for i := 0; i < len(subArgs); i++ {
		a := subArgs[i]
		switch {
		case a == "-h" || a == "--help":
			printAuthHelp()
			return nil
		case a == "--force" || a == "-f":
			force = true
		case a == "--within" && i+1 < len(subArgs):
			i++
			d, err := time.ParseDuration(subArgs[i])
			if err != nil {
				return fmt.Errorf("invalid --within duration %q: %w", subArgs[i], err)
			}
			if d < 0 {
				return fmt.Errorf("invalid --within duration %q: must not be negative", subArgs[i])
			}
			window = d
		case a == "--no-extensions":
			cliArgs.NoExtensions = true
		case (a == "--extension" || a == "-e") && i+1 < len(subArgs):
			i++
			cliArgs.Extensions = append(cliArgs.Extensions, subArgs[i])
		case (a == "--disable-extension" || a == "-d") && i+1 < len(subArgs):
			i++
			cliArgs.DisabledExtensions = append(cliArgs.DisabledExtensions, subArgs[i])
		case a == "--debug":
			cliArgs.Debug = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag for 'fir auth refresh': %s", a)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 1 {
		return fmt.Errorf("fir auth refresh: expected at most one provider id, got %d arguments", len(positional))
	}
	providerID := defaultAuthProvider
	if len(positional) == 1 {
		providerID = positional[0]
	}

	// Subcommand dispatch happens before run()'s log init, so we own it here.
	debugEnabled := cliArgs.Debug || os.Getenv("FIR_DEBUG") == "1"
	debugPath := os.Getenv("FIR_DEBUG_LOG")
	if debugPath == "" {
		debugPath = filepath.Join(resolveAgentDir(), "debug.log")
	}
	logCleanup, err := firlog.Init(debugEnabled, debugPath, resolveDebugLogConfig())
	if err != nil {
		return fmt.Errorf("init debug log: %w", err)
	}
	defer logCleanup()

	providers.RegisterDefaultProviders()

	// OAuth providers are declared by auth extensions, so the refresh path
	// needs the same minimal session `fir login` uses to load them.
	cleanup, err := startLoginSession(cliArgs)
	if err != nil {
		return err
	}
	defer cleanup()

	if ai.GetOAuthProvider(providerID) == nil {
		fmt.Fprintf(os.Stderr, "Unknown OAuth provider: %s\n\nAvailable providers:\n", providerID)
		for _, p := range ai.GetOAuthProviders() {
			fmt.Fprintf(os.Stderr, "  %s - %s\n", p.ID(), p.Name())
		}
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	// Fresh storage handle so we read the file as it is right now, and write
	// through the same flock-protected path everything else uses.
	authStorage := auth.NewAuthStorage(filepath.Join(resolveAgentDir(), "auth.json"))

	ctx, cancel := context.WithTimeout(context.Background(), authRefreshTimeout)
	defer cancel()

	results := authStorage.RefreshAccounts(ctx, providerID, window, force)
	if len(results) == 0 {
		return fmt.Errorf("no stored accounts for provider %q — run: fir login %s", providerID, providerID)
	}
	return reportRefreshResults(os.Stdout, results)
}

// reportRefreshResults prints one tab-separated line per slot and returns a
// non-nil error when any slot failed, so cron and journal can see it.
func reportRefreshResults(w io.Writer, results []auth.RefreshResult) error {
	failed := 0
	for _, r := range results {
		expiry := "expires=-\tin=-"
		if t := r.ExpiresAt(); !t.IsZero() {
			expiry = fmt.Sprintf("expires=%s\tin=%s",
				t.UTC().Format(time.RFC3339), time.Until(t).Round(time.Second))
		}
		detail := ""
		switch {
		case r.Err != nil:
			detail = "\t" + r.Err.Error()
		case r.Reason != "":
			detail = "\t" + r.Reason
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\n", r.SlotKey, r.Label, r.Outcome, expiry, detail)
		if r.Outcome == auth.OutcomeFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d account(s) failed to refresh", failed, len(results))
	}
	return nil
}
