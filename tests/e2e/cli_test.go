//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// Section 3, 5, 9, 10: CLI flag tests

func TestCLI_Help(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--help")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	for _, want := range []string{"Usage", "--provider", "--model"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in help output: %s", want, out)
		}
	}
}

func TestCLI_Version(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--version")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	if !strings.Contains(strings.ToLower(out), "fir") {
		t.Fatalf("expected 'fir' in version output: %s", out)
	}
}

func TestCLI_ListModels(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	if !strings.Contains(out, "/") {
		t.Fatalf("expected provider/model format in output: %s", out)
	}
}

// Section 5: Extended CLI flag tests

func TestCLI_ListModelsIncludesGemini25Pro(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "google/gemini-2.5-pro" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("google/gemini-2.5-pro not found in: %s", out)
	}
}

// Section 9: Theme flag tests

func TestCLI_ThemeValidDir(t *testing.T) {
	dir := t.TempDir()
	out, code := runFir(t, "", 10*time.Second, nil, "--theme", dir, "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

func TestCLI_ThemeNonexistentPath(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--theme", "/nonexistent/path/that/does/not/exist", "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

func TestCLI_NoThemes(t *testing.T) {
	out, code := runFir(t, "", 10*time.Second, nil, "--no-themes", "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

func TestCLI_CustomThemeFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "myTheme.json", `{"name":"myTheme","description":"Custom test theme"}`); err != nil {
		t.Fatal(err)
	}
	out, code := runFir(t, "", 10*time.Second, nil, "--theme", dir, "--list-models")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

// Section 10: ResolveCliModel tests

func TestCLI_ModelPrefixMatch(t *testing.T) {
	agentDir := makeAgentDir(t, `{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"special-match-model","name":"Special Match Model","contextWindow":128000,"maxTokens":4096}]}}}`)
	out, code := runFirWithAgentDir(t, agentDir, `{"id":"1","type":"get_state"}`, 10*time.Second, "--provider", "mock", "--model", "special-match", "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_state"
	})
	if resp == nil {
		t.Fatalf("no get_state response: %s", out)
	}
	if getNestedString(resp, "success") != "true" {
		t.Fatalf("get_state failed: %s", out)
	}
	modelID := getNestedString(resp, "data.model.id")
	if modelID != "special-match-model" {
		t.Fatalf("expected model id 'special-match-model', got: %s", modelID)
	}
}

func TestCLI_ModelProviderSlashNotation(t *testing.T) {
	agentDir := makeAgentDir(t, `{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"mock-model","name":"Mock Model","contextWindow":128000,"maxTokens":4096}]}}}`)
	out, code := runFirWithAgentDir(t, agentDir, `{"id":"1","type":"get_state"}`, 10*time.Second, "--model", "mock/mock-model", "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_state"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_state failed: %s", out)
	}
	if getNestedString(resp, "data.model.id") != "mock-model" {
		t.Fatalf("expected mock-model, got: %s", getNestedString(resp, "data.model.id"))
	}
}

func TestCLI_ModelThinkingLevelSuffix(t *testing.T) {
	agentDir := makeAgentDir(t, `{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"think-model","name":"Think Model","contextWindow":128000,"maxTokens":4096,"reasoning":true}]}}}`)
	out, code := runFirWithAgentDir(t, agentDir, `{"id":"1","type":"get_state"}`, 10*time.Second, "--model", "think-model:high", "--mode", "rpc", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	resp := findJSONLine(lines, func(m map[string]any) bool {
		return getNestedString(m, "command") == "get_state"
	})
	if resp == nil || getNestedString(resp, "success") != "true" {
		t.Fatalf("get_state failed: %s", out)
	}
	if getNestedString(resp, "data.model.id") != "think-model" {
		t.Fatalf("expected think-model, got: %s", getNestedString(resp, "data.model.id"))
	}
	if getNestedString(resp, "data.thinkingLevel") != "high" {
		t.Fatalf("expected thinkingLevel high, got: %s", getNestedString(resp, "data.thinkingLevel"))
	}
}
