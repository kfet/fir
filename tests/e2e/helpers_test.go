//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	firBinary    string
	mockPort     string
	mockServer   *http.Server
	mockListener net.Listener
	mockAgentDir string
	projectRoot  string
)

func TestMain(m *testing.M) {
	var err error
	projectRoot, err = filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to find project root: %v\n", err)
		os.Exit(1)
	}

	// Build fir binary
	firBinary = filepath.Join(projectRoot, "bin", "fir-e2e")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", firBinary, "./cmd/fir/")
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build fir: %v\n", err)
		os.Exit(1)
	}

	// Start mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleCompletions)

	mockListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start mock server: %v\n", err)
		os.Exit(1)
	}
	mockPort = fmt.Sprintf("%d", mockListener.Addr().(*net.TCPAddr).Port)
	mockServer = &http.Server{Handler: mux}
	go mockServer.Serve(mockListener)

	// Create mock agent dir with two models
	mockAgentDir, err = os.MkdirTemp("", "fir-e2e-mock-agent")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create mock agent dir: %v\n", err)
		os.Exit(1)
	}
	modelsJSON := fmt.Sprintf(`{
  "providers": {
    "mock": {
      "baseUrl": "http://localhost:%s",
      "apiKey": "mock-key",
      "api": "openai-completions",
      "models": [
        {"id": "mock-model", "name": "Mock Model", "contextWindow": 128000, "maxTokens": 4096},
        {"id": "mock-model-2", "name": "Mock Model 2", "contextWindow": 128000, "maxTokens": 4096}
      ]
    }
  }
}`, mockPort)
	os.WriteFile(filepath.Join(mockAgentDir, "models.json"), []byte(modelsJSON), 0o644)

	code := m.Run()

	// Teardown
	mockServer.Close()
	os.RemoveAll(mockAgentDir)
	os.Remove(firBinary)
	os.Exit(code)
}

// runFir runs the fir binary with given args, optional stdin, and env overrides.
// Returns combined stdout+stderr and the exit code.
func runFir(t *testing.T, stdin string, timeout time.Duration, env map[string]string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(firBinary, args...)
	cmd.Dir = projectRoot
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	// Build env
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	timer := time.AfterFunc(timeout, func() { cmd.Process.Kill() })
	out, err := cmd.CombinedOutput()
	timer.Stop()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return string(out), exitCode
}

// runFirMock runs fir with mock provider settings.
func runFirMock(t *testing.T, stdin string, timeout time.Duration, args ...string) (string, int) {
	t.Helper()
	env := map[string]string{"FIR_AGENT_DIR": mockAgentDir}
	allArgs := append([]string{"--provider", "mock", "--model", "mock-model"}, args...)
	return runFir(t, stdin, timeout, env, allArgs...)
}

// runFirMockDir runs fir with mock provider settings in a specific directory.
func runFirMockDir(t *testing.T, dir string, stdin string, timeout time.Duration, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(firBinary, append([]string{"--provider", "mock", "--model", "mock-model"}, args...)...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(), "FIR_AGENT_DIR="+mockAgentDir)
	timer := time.AfterFunc(timeout, func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	out, err := cmd.CombinedOutput()
	timer.Stop()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return string(out), exitCode
}

// runFirWithAgentDir runs fir with a custom agent dir.
func runFirWithAgentDir(t *testing.T, agentDir string, stdin string, timeout time.Duration, args ...string) (string, int) {
	t.Helper()
	env := map[string]string{"FIR_AGENT_DIR": agentDir}
	return runFir(t, stdin, timeout, env, args...)
}

// makeAgentDir creates a temp agent dir with given models.json content.
func makeAgentDir(t *testing.T, modelsJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(modelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// parseJSONLines parses newline-delimited JSON from output, skipping non-JSON lines.
func parseJSONLines(output string) []map[string]any {
	var results []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			results = append(results, obj)
		}
	}
	return results
}

// assertNoPanel checks there's no panic in output.
func assertNoPanic(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, "panic:") {
		t.Fatalf("output contains panic:\n%s", output)
	}
	if strings.Contains(output, "runtime error:") {
		t.Fatalf("output contains runtime error:\n%s", output)
	}
}

// findJSONLine finds first JSON line matching a predicate.
func findJSONLine(lines []map[string]any, pred func(map[string]any) bool) map[string]any {
	for _, l := range lines {
		if pred(l) {
			return l
		}
	}
	return nil
}

// getNestedString gets a dot-path value like "data.model.id" from a map.
func getNestedString(m map[string]any, path string) string {
	parts := strings.Split(path, ".")
	current := any(m)
	for _, p := range parts {
		mm, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mm[p]
	}
	if s, ok := current.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", current)
}

// getNestedBool gets a nested boolean value.
func getNestedBool(m map[string]any, path string) (bool, bool) {
	parts := strings.Split(path, ".")
	current := any(m)
	for _, p := range parts {
		mm, ok := current.(map[string]any)
		if !ok {
			return false, false
		}
		current = mm[p]
	}
	if b, ok := current.(bool); ok {
		return b, true
	}
	return false, false
}

// runFirWithDelay runs fir with stdin that stays open for a delay after writing input.
func runFirWithDelay(t *testing.T, stdin string, stdinDelay time.Duration, timeout time.Duration, env map[string]string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(firBinary, args...)
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Write input then wait before closing stdin
	if stdin != "" {
		io.WriteString(stdinPipe, stdin)
	}
	time.Sleep(stdinDelay)
	stdinPipe.Close()

	timer := time.AfterFunc(timeout, func() { cmd.Process.Kill() })
	err = cmd.Wait()
	timer.Stop()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), exitCode
}

// writeFile writes content to a file in the given directory.
func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// stripEnvKeys returns env with specific keys removed.
func stripEnvKeys(keys ...string) []string {
	skip := make(map[string]bool)
	for _, k := range keys {
		skip[k] = true
	}
	var result []string
	for _, e := range os.Environ() {
		k := strings.SplitN(e, "=", 2)[0]
		if !skip[k] {
			result = append(result, e)
		}
	}
	return result
}
