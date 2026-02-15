// Package sandbox provides OS-level sandboxing for bash commands.
//
// It wraps the built-in bash tool with configurable filesystem and network
// restrictions using sandbox-exec (macOS) or bubblewrap (Linux).
//
// Config files (merged, project takes precedence):
//   - ~/.tau/agent/sandbox.json (global)
//   - <cwd>/.tau/sandbox.json (project-local)
//
// Example .tau/sandbox.json:
//
//	{
//	  "enabled": true,
//	  "network": {
//	    "allowedDomains": ["github.com", "*.github.com"],
//	    "deniedDomains": []
//	  },
//	  "filesystem": {
//	    "denyRead": ["~/.ssh", "~/.aws"],
//	    "allowWrite": [".", "/tmp"],
//	    "denyWrite": [".env"]
//	  }
//	}
//
// Usage:
//   - Import to enable: import _ "github.com/kfet/tau/pkg/extensions/sandbox"
//   - Disable via flag: tau --no-sandbox
//   - Show config: /sandbox command
//
// Note: This is a framework. The actual sandboxing integration (sandbox-exec,
// bubblewrap) would need to be implemented per-platform.
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/tau/pkg/extension"
)

// SandboxConfig defines the sandbox configuration.
type SandboxConfig struct {
	Enabled    *bool           `json:"enabled,omitempty"`
	Network    *NetworkConfig  `json:"network,omitempty"`
	Filesystem *FSConfig       `json:"filesystem,omitempty"`
}

// NetworkConfig defines network restrictions.
type NetworkConfig struct {
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	DeniedDomains  []string `json:"deniedDomains,omitempty"`
}

// FSConfig defines filesystem restrictions.
type FSConfig struct {
	DenyRead   []string `json:"denyRead,omitempty"`
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
}

var defaultConfig = SandboxConfig{
	Enabled: boolPtr(true),
	Network: &NetworkConfig{
		AllowedDomains: []string{
			"npmjs.org", "*.npmjs.org", "registry.npmjs.org",
			"registry.yarnpkg.com",
			"pypi.org", "*.pypi.org",
			"github.com", "*.github.com", "api.github.com",
			"raw.githubusercontent.com",
		},
	},
	Filesystem: &FSConfig{
		DenyRead:   []string{"~/.ssh", "~/.aws", "~/.gnupg"},
		AllowWrite: []string{".", "/tmp"},
		DenyWrite:  []string{".env", ".env.*", "*.pem", "*.key"},
	},
}

func init() {
	extension.Register("sandbox", func(api extension.API) {
		api.RegisterFlag("no-sandbox", extension.Flag{
			Description: "Disable OS-level sandboxing for bash commands",
			Type:        "boolean",
			Default:     false,
		})

		var (
			sandboxEnabled bool
			config         SandboxConfig
		)

		api.On("session_start", func(event *extension.Event, ctx extension.Context) (any, error) {
			noSandbox, _ := api.GetFlag("no-sandbox").(bool)
			if noSandbox {
				sandboxEnabled = false
				ctx.UI().Notify("Sandbox disabled via --no-sandbox", "warning")
				return nil, nil
			}

			config = loadConfig(ctx.Cwd())
			if config.Enabled != nil && !*config.Enabled {
				sandboxEnabled = false
				ctx.UI().Notify("Sandbox disabled via config", "info")
				return nil, nil
			}

			sandboxEnabled = true

			networkCount := 0
			if config.Network != nil {
				networkCount = len(config.Network.AllowedDomains)
			}
			writeCount := 0
			if config.Filesystem != nil {
				writeCount = len(config.Filesystem.AllowWrite)
			}

			ctx.UI().SetStatus("sandbox",
				fmt.Sprintf("🔒 Sandbox: %d domains, %d write paths", networkCount, writeCount))
			ctx.UI().Notify("Sandbox initialized", "info")
			return nil, nil
		})

		api.On("session_shutdown", func(event *extension.Event, ctx extension.Context) (any, error) {
			if sandboxEnabled {
				// Reset sandbox state
				sandboxEnabled = false
			}
			return nil, nil
		})

		// Register the /sandbox command to show configuration.
		api.RegisterCommand("sandbox", extension.Command{
			Description: "Show sandbox configuration",
			Handler: func(args string, ctx extension.CommandContext) error {
				if !sandboxEnabled {
					ctx.UI().Notify("Sandbox is disabled", "info")
					return nil
				}

				lines := []string{
					"Sandbox Configuration:",
					"",
				}

				if config.Network != nil {
					lines = append(lines, "Network:")
					lines = append(lines, fmt.Sprintf("  Allowed: %s",
						joinOrNone(config.Network.AllowedDomains)))
					lines = append(lines, fmt.Sprintf("  Denied: %s",
						joinOrNone(config.Network.DeniedDomains)))
					lines = append(lines, "")
				}

				if config.Filesystem != nil {
					lines = append(lines, "Filesystem:")
					lines = append(lines, fmt.Sprintf("  Deny Read: %s",
						joinOrNone(config.Filesystem.DenyRead)))
					lines = append(lines, fmt.Sprintf("  Allow Write: %s",
						joinOrNone(config.Filesystem.AllowWrite)))
					lines = append(lines, fmt.Sprintf("  Deny Write: %s",
						joinOrNone(config.Filesystem.DenyWrite)))
				}

				ctx.UI().Notify(strings.Join(lines, "\n"), "info")
				return nil
			},
		})

		// Best-effort command inspection: checks if denied paths appear literally
		// in the bash command string. This is NOT a security boundary — it can be
		// trivially bypassed via shell variable expansion ($HOME/.ssh), quoting,
		// backslash escaping (~/.s\sh), or any other shell indirection.
		//
		// For real sandboxing, the bash tool should be wrapped with OS-level
		// enforcement (sandbox-exec on macOS, bubblewrap on Linux) that restricts
		// the child process's filesystem access at the kernel level.
		api.On("tool_call", func(event *extension.Event, ctx extension.Context) (any, error) {
			if !sandboxEnabled {
				return nil, nil
			}
			if event.ToolCall == nil || event.ToolCall.ToolName != "bash" {
				return nil, nil
			}

			command, _ := event.ToolCall.Input["command"].(string)
			if command == "" {
				return nil, nil
			}

			// Best-effort heuristic check — see comment above for limitations.
			if config.Filesystem != nil {
				for _, denied := range config.Filesystem.DenyRead {
					expanded := expandPath(denied)
					if strings.Contains(command, expanded) {
						return &extension.ToolCallResult{
							Block:  true,
							Reason: fmt.Sprintf("Sandbox: reading %s is denied", denied),
						}, nil
					}
				}
			}

			return nil, nil
		})
	})
}

// loadConfig loads and merges sandbox configuration.
func loadConfig(cwd string) SandboxConfig {
	home, _ := os.UserHomeDir()
	globalPath := filepath.Join(home, ".tau", "agent", "sandbox.json")
	projectPath := filepath.Join(cwd, ".tau", "sandbox.json")

	result := defaultConfig

	if data, err := os.ReadFile(globalPath); err == nil {
		var global SandboxConfig
		if json.Unmarshal(data, &global) == nil {
			result = mergeConfig(result, global)
		}
	}

	if data, err := os.ReadFile(projectPath); err == nil {
		var project SandboxConfig
		if json.Unmarshal(data, &project) == nil {
			result = mergeConfig(result, project)
		}
	}

	return result
}

func mergeConfig(base, override SandboxConfig) SandboxConfig {
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	if override.Network != nil {
		if base.Network == nil {
			base.Network = &NetworkConfig{}
		}
		if override.Network.AllowedDomains != nil {
			base.Network.AllowedDomains = override.Network.AllowedDomains
		}
		if override.Network.DeniedDomains != nil {
			base.Network.DeniedDomains = override.Network.DeniedDomains
		}
	}
	if override.Filesystem != nil {
		if base.Filesystem == nil {
			base.Filesystem = &FSConfig{}
		}
		if override.Filesystem.DenyRead != nil {
			base.Filesystem.DenyRead = override.Filesystem.DenyRead
		}
		if override.Filesystem.AllowWrite != nil {
			base.Filesystem.AllowWrite = override.Filesystem.AllowWrite
		}
		if override.Filesystem.DenyWrite != nil {
			base.Filesystem.DenyWrite = override.Filesystem.DenyWrite
		}
	}
	return base
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

func boolPtr(b bool) *bool {
	return &b
}
