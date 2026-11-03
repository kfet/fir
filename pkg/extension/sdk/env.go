package sdk

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
//
// FIR_BIN names the running fir binary so an extension can call back into
// the CLI (e.g. `$FIR_BIN sessions list`) and get the same build and config
// as its host. An inherited FIR_BIN is left alone, and none is set when the
// host is a `go test` binary (calling it would re-run the test suite).
func SDKEnv(basePath string) []string {
	env := []string{
		prependEnv("PYTHONPATH", filepath.Join(basePath, "python")),
		prependEnv("NODE_PATH", filepath.Join(basePath, "node")),
		prependEnv("RUBYLIB", filepath.Join(basePath, "ruby")),
		"FIR_HOST_PID=" + strconv.Itoa(os.Getpid()),
	}
	if bin := firBin(); bin != "" {
		env = append(env, "FIR_BIN="+bin)
	}
	return env
}

// executable is os.Executable, swappable in tests.
var executable = os.Executable

// firBin returns the path FIR_BIN should carry, or "" to set none.
func firBin() string {
	if os.Getenv("FIR_BIN") != "" {
		return ""
	}
	exe, err := executable()
	if err != nil || strings.HasSuffix(exe, ".test") || strings.HasSuffix(exe, ".test.exe") {
		return ""
	}
	return exe
}

func prependEnv(key, dir string) string {
	if existing := os.Getenv(key); existing != "" {
		return key + "=" + dir + ":" + existing
	}
	return key + "=" + dir
}
