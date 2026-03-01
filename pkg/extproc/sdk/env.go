package sdk

import "path/filepath"

// SDKEnv returns environment variable entries that should be prepended
// to a spawned extension process's environment so it can import the
// extracted SDK stubs.
//
// basePath is the value returned by EnsureExtracted.
// Node and Ruby paths are included for forward compatibility even if
// those SDKs have not been created yet.
func SDKEnv(basePath string) []string {
	return []string{
		"PYTHONPATH=" + filepath.Join(basePath, "python"),
		"NODE_PATH=" + filepath.Join(basePath, "node"),
		"RUBYLIB=" + filepath.Join(basePath, "ruby"),
	}
}
