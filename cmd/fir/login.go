package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/platform"
	"github.com/kfet/fir/pkg/auth"
)

// runLogin runs the interactive OAuth login flow for a given provider and exits.
// This is invoked by --login <provider> and is used by ACP terminal auth.
func runLogin(args *Args) error {
	providerID := args.Login

	provider := oauth.GetProvider(providerID)
	if provider == nil {
		// List available providers.
		providers := oauth.GetProviders()
		fmt.Fprintf(os.Stderr, "Unknown OAuth provider: %s\n\nAvailable providers:\n", providerID)
		for _, p := range providers {
			fmt.Fprintf(os.Stderr, "  %s - %s\n", p.ID(), p.Name())
		}
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	agentDir := resolveAgentDir()
	authStorage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))

	fmt.Fprintf(os.Stderr, "Logging in to %s...\n", provider.Name())

	callbacks := oauth.LoginCallbacks{
		OnAuth: func(info oauth.AuthInfo) {
			browserOpened := platform.OpenBrowser(info.URL) == nil
			if browserOpened {
				fmt.Fprintf(os.Stderr, "Opening browser… if it doesn't appear, visit:\n%s\n", info.URL)
			} else {
				fmt.Fprintf(os.Stderr, "Open this URL to authenticate:\n%s\n", info.URL)
			}
			if info.Instructions != "" {
				fmt.Fprintf(os.Stderr, "%s\n", info.Instructions)
			}
		},
		OnPrompt: func(prompt oauth.Prompt) (string, error) {
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
		OnProgress: func(message string) {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		},
	}

	err := authStorage.Login(providerID, callbacks)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Logged in to %s. Credentials saved.\n", provider.Name())
	return nil
}
