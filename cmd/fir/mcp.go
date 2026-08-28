package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/pinoauth"
)

// runMCPSubcommand implements `fir mcp <verb>`.
//
// It exists so an MCP server's OAuth token can be minted ahead of time, from a
// plain terminal, instead of during a session: MCP servers connect from
// background goroutines (potentially before the TUI is up), so fir never opens
// a browser on its own. A server that needs a login reports
// `run: fir mcp login <server>` — this is that command.
//
// Usage:
//
//	fir mcp                     # list configured servers and auth state
//	fir mcp list
//	fir mcp login <server>
//	fir mcp logout <server>
func runMCPSubcommand() error {
	args := os.Args[2:] // skip "fir mcp"
	verb := "list"
	if len(args) > 0 {
		verb = args[0]
	}
	rest := args[1:]

	switch verb {
	case "list":
		if len(rest) > 0 {
			return fmt.Errorf("fir mcp list: unexpected argument %q", rest[0])
		}
		return runMCPList()
	case "login":
		if len(rest) != 1 {
			return fmt.Errorf("usage: fir mcp login <server>")
		}
		return runMCPLogin(rest[0])
	case "logout":
		if len(rest) != 1 {
			return fmt.Errorf("usage: fir mcp logout <server>")
		}
		return runMCPLogout(rest[0])
	default:
		return fmt.Errorf("unknown subcommand %q; usage: fir mcp [list|login <server>|logout <server>]", verb)
	}
}

// mcpContext loads the merged MCP config for the current directory plus the
// shared auth storage.
func mcpContext() (map[string]mcp.ServerConfig, *auth.AuthStorage, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve working directory: %w", err)
	}
	cfg, err := mcp.LoadDefaultConfigs(cwd)
	if err != nil {
		return nil, nil, err
	}
	storage := auth.NewAuthStorage(filepath.Join(resolveAgentDir(), "auth.json"))
	return cfg.MCPServers, storage, nil
}

// sortedServerNames returns the configured server names in display order.
func sortedServerNames(servers map[string]mcp.ServerConfig) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runMCPList() error {
	servers, storage, err := mcpContext()
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured.")
		return nil
	}
	mgr := mcp.NewManager(servers, false)
	mgr.SetAuth(storage)
	fmt.Println("MCP servers:")
	for _, name := range sortedServerNames(servers) {
		cfg := servers[name]
		target := cfg.Command
		transport := cfg.Transport
		if transport == "" {
			transport = "stdio"
		}
		if cfg.URL != "" {
			target = cfg.URL
		}
		fmt.Printf("  %-20s %-11s %s\n", name, transport, target)
		if status := mgr.AuthStatus(name); status != "" {
			fmt.Printf("  %-20s %-11s %s\n", "", "", status)
		}
	}
	return nil
}

func runMCPLogin(serverName string) error {
	servers, storage, err := mcpContext()
	if err != nil {
		return err
	}
	if _, ok := servers[serverName]; !ok {
		return unknownMCPServer(serverName, servers)
	}
	mgr := mcp.NewManager(servers, false)
	mgr.SetAuth(storage)

	fmt.Fprintf(os.Stderr, "Logging in to MCP server %q…\n", serverName)
	if err := mgr.LoginServer(context.Background(), serverName, cliLoginCallbacks()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in to MCP server %q. Credentials saved.\n", serverName)
	fmt.Fprintf(os.Stderr, "Any fir session already running: use /mcp reload to pick this up.\n")
	return nil
}

func runMCPLogout(serverName string) error {
	servers, storage, err := mcpContext()
	if err != nil {
		return err
	}
	mgr := mcp.NewManager(servers, false)
	mgr.SetAuth(storage)
	if err := mgr.LogoutServer(serverName); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Removed stored credentials for MCP server %q.\n", serverName)
	return nil
}

func unknownMCPServer(serverName string, servers map[string]mcp.ServerConfig) error {
	names := sortedServerNames(servers)
	if len(names) == 0 {
		return fmt.Errorf("no MCP servers configured; add one to .fir/mcp.json")
	}
	return fmt.Errorf("unknown MCP server %q; configured: %s", serverName, strings.Join(names, ", "))
}

// cliLoginCallbacks builds the terminal OAuth UI, mirroring `fir login`.
func cliLoginCallbacks() pinoauth.LoginCallbacks {
	return pinoauth.LoginCallbacks{
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
			fmt.Fprintf(os.Stderr, "\r\033[K")
		},
		OnProgress: func(message string) {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		},
	}
}
