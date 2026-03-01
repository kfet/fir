package sdk

import (
	"os"
	"path/filepath"
)

// SDKEnv returns environment variable entries that prepend the SDK paths
// to any existing values, so user-installed packages are still accessible.
//
// basePath is the value returned by EnsureExtracted.
// Node and Ruby paths are included for forward compatibility.
func SDKEnv(basePath string) []string {
	return []string{
		prependEnv("PYTHONPATH", filepath.Join(basePath, "python")),
		prependEnv("NODE_PATH", filepath.Join(basePath, "node")),
		prependEnv("RUBYLIB", filepath.Join(basePath, "ruby")),
	}
}

func prependEnv(key, dir string) string {
	if existing := os.Getenv(key); existing != "" {
		return key + "=" + dir + ":" + existing
	}
	return key + "=" + dir
}
