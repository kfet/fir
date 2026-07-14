package sdk

import (
	"os"
	"path/filepath"
	"strconv"
)

// SDKEnv returns environment variable entries that prepend the SDK paths
// to any existing values, so user-installed packages are still accessible.
//
// basePath is the value returned by EnsureExtracted.
// Node and Ruby paths are included for forward compatibility.
//
// FIR_HOST_PID carries this fir host process's own pid down to every
// extension. Extensions cannot derive it reliably themselves: under the
// forkserver architecture the extension is fork()'d by the python forkserver,
// so os.getppid() yields the forkserver pid, not fir. The observe sidecar
// records this as host_pid so `fir observe` and stop_session signal the real
// binary. Callers of SDKEnv run in the host process, so os.Getpid() is the
// host pid.
func SDKEnv(basePath string) []string {
	return []string{
		prependEnv("PYTHONPATH", filepath.Join(basePath, "python")),
		prependEnv("NODE_PATH", filepath.Join(basePath, "node")),
		prependEnv("RUBYLIB", filepath.Join(basePath, "ruby")),
		"FIR_HOST_PID=" + strconv.Itoa(os.Getpid()),
	}
}

func prependEnv(key, dir string) string {
	if existing := os.Getenv(key); existing != "" {
		return key + "=" + dir + ":" + existing
	}
	return key + "=" + dir
}
