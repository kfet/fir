// Ported from: packages/coding-agent/src/core/resolve-config-value.ts
// Upstream hash: 1caadb2e
package core

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	commandCacheMu sync.RWMutex
	commandCache   = make(map[string]*string) // nil *string = command returned empty
)

// ResolveConfigValue resolves a config value (API key, header value, etc.) to an actual value.
// - If starts with "!", executes the rest as a shell command and uses stdout (cached)
// - Otherwise checks environment variable first, then treats as literal (not cached)
// Returns empty string if resolution fails.
func ResolveConfigValue(config string) string {
	if config == "" {
		return ""
	}
	if strings.HasPrefix(config, "!") {
		return executeConfigCommand(config)
	}
	// Check if it's an env var name
	if envVal := os.Getenv(config); envVal != "" {
		return envVal
	}
	// Treat as literal
	return config
}

func executeConfigCommand(commandConfig string) string {
	commandCacheMu.RLock()
	cached, ok := commandCache[commandConfig]
	commandCacheMu.RUnlock()
	if ok {
		if cached == nil {
			return ""
		}
		return *cached
	}

	command := commandConfig[1:] // strip leading "!"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = nil
	output, err := cmd.Output()

	commandCacheMu.Lock()
	defer commandCacheMu.Unlock()

	if err != nil {
		commandCache[commandConfig] = nil
		return ""
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		commandCache[commandConfig] = nil
		return ""
	}
	commandCache[commandConfig] = &result
	return result
}

// ResolveHeaders resolves all header values using the same resolution logic as API keys.
// Returns nil if no headers resolve to non-empty values.
func ResolveHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	resolved := make(map[string]string)
	for key, value := range headers {
		if rv := ResolveConfigValue(value); rv != "" {
			resolved[key] = rv
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// ClearConfigValueCache clears the command result cache. Exported for testing.
func ClearConfigValueCache() {
	commandCacheMu.Lock()
	defer commandCacheMu.Unlock()
	commandCache = make(map[string]*string)
}
