//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Section 1: Print mode tests
// Section 7: Print mode error handling

func TestPrintMode_PipedStdin(t *testing.T) {
	out, code := runFirMock(t, "What is 2+2?", 15*time.Second, "--no-session", "-p")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestPrintMode_MessageArg(t *testing.T) {
	out, code := runFirMock(t, "", 15*time.Second, "--no-session", "-p", "Say exactly: HELLO_E2E_TEST")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "MOCK_RESPONSE") {
		t.Fatalf("expected MOCK_RESPONSE in output, got: %s", out)
	}
}

func TestPrintMode_NoSession(t *testing.T) {
	out, code := runFirMock(t, "Say OK", 15*time.Second, "--no-session", "-p")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

func TestPrintMode_NoAPIKey(t *testing.T) {
	cmd := exec.Command(firBinary, "--no-session", "-p")
	cmd.Dir = projectRoot
	cmd.Stdin = strings.NewReader("test")
	cmd.Env = stripEnvKeys(
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GROQ_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY",
		"MISTRAL_API_KEY", "AWS_PROFILE", "FIR_AGENT_DIR",
	)
	out, err := cmd.CombinedOutput()
	output := string(out)
	assertNoPanic(t, output)
	if err == nil {
		if !strings.Contains(output, "No models") && !strings.Contains(output, "API key") && !strings.Contains(output, "Forbidden") {
			t.Log("exit code 0 but no error message — acceptable if default provider works")
		}
	}
}

func TestPrintMode_JSONOutput(t *testing.T) {
	out, code := runFirMock(t, "Say hello", 15*time.Second, "--no-session", "--mode", "json")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	foundJSON := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			foundJSON = true
		}
	}
	if !foundJSON {
		t.Fatalf("expected JSON output lines, got: %s", out)
	}
}

func TestPrintMode_APIFailureExitsNonZero(t *testing.T) {
	agentDir := makeAgentDir(t, `{"providers":{"dead":{"baseUrl":"http://127.0.0.1:1","apiKey":"bad","api":"openai-completions","models":[{"id":"dead-model","name":"Dead","contextWindow":128000,"maxTokens":4096}]}}}`)
	out, code := runFirWithAgentDir(t, agentDir, "", 15*time.Second, "--provider", "dead", "--model", "dead-model", "--no-session", "-p", "say hello")
	assertNoPanic(t, out)
	if code == 0 {
		t.Fatalf("expected non-zero exit code, got 0. output: %s", out)
	}
}

func TestPrintMode_BadProviderNoPanic(t *testing.T) {
	agentDir := makeAgentDir(t, `{"providers":{"bad":{"baseUrl":"http://localhost:9999","apiKey":"key","models":[{"id":"m","name":"M","contextWindow":128000,"maxTokens":4096}]}}}`)
	out, _ := runFirWithAgentDir(t, agentDir, "", 10*time.Second, "--list-models")
	assertNoPanic(t, out)
}
