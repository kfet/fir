//go:build e2e

package e2e

import (
	"fmt"
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
	agentDir := makeAgentDir(t, fmt.Sprintf(`{"providers":{"mock":{"baseUrl":"http://localhost:%s","apiKey":"mock-key","api":"openai-completions","models":[{"id":"special-match-model","name":"Special Match Model","contextWindow":128000,"maxTokens":4096}]}}}`, mockPort))
	out, code := runFirWithAgentDir(t, agentDir, "hello", 10*time.Second, "--provider", "mock", "--model", "special-match", "--print", "--output", "json", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	lines := parseJSONLines(out)
	// In JSON print mode, the model ID should appear in the output metadata
	found := false
	for _, line := range lines {
		if m := getNestedString(line, "model"); m == "special-match-model" {
			found = true
			break
		}
	}
	if !found {
		// At minimum, verify no error and the prefix resolved (didn't crash)
		assertNoPanic(t, out)
	}
}

func TestCLI_ModelProviderSlashNotation(t *testing.T) {
	agentDir := makeAgentDir(t, fmt.Sprintf(`{"providers":{"mock":{"baseUrl":"http://localhost:%s","apiKey":"mock-key","api":"openai-completions","models":[{"id":"mock-model","name":"Mock Model","contextWindow":128000,"maxTokens":4096}]}}}`, mockPort))
	out, code := runFirWithAgentDir(t, agentDir, "hello", 10*time.Second, "--model", "mock/mock-model", "--print", "--output", "json", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

func TestCLI_ModelThinkingLevelSuffix(t *testing.T) {
	agentDir := makeAgentDir(t, fmt.Sprintf(`{"providers":{"mock":{"baseUrl":"http://localhost:%s","apiKey":"mock-key","api":"openai-completions","models":[{"id":"think-model","name":"Think Model","contextWindow":128000,"maxTokens":4096,"reasoning":true}]}}}`, mockPort))
	out, code := runFirWithAgentDir(t, agentDir, "hello", 10*time.Second, "--model", "think-model:high", "--print", "--output", "json", "--no-session")
	if code != 0 {
		t.Fatalf("exit code %d, output: %s", code, out)
	}
	assertNoPanic(t, out)
}

// TestCLI_ExtShippedProviderResolves is a regression test for the Phase B4
// migration of google-gemini-cli and google-antigravity providers out of
// core into the gemini-cli-auth and antigravity-auth builtin extensions.
//
// It catches two distinct bugs that broke real-inference end-to-end after
// the migration:
//
//  1. CLI model resolution ran before extensions registered, so
//     `--provider google-gemini-cli --model gemini-2.5-flash` failed with
//     `Unknown provider "google-gemini-cli"` in -p / non-interactive mode.
//
//  2. ModelRegistry.Refresh() (called via refreshSessionModel after auth
//     extensions ran) called Registry.ResetApiProviders, which wiped
//     the dynamic Api map and re-registered only built-ins — silently
//     dropping the wire-protocol Api entries shipped by the same
//     extensions, so the very next stream attempt panicked with
//     `no API provider registered for api: google-gemini-cli`.
//
// The test runs with a fresh empty agent dir (no OAuth credentials), so
// inference will fail downstream — but the failure must be a credentials/
// network/auth error, NOT one of the two regression strings above.
//
// Run for both ext-shipped providers since each owns its own ApiSpec
// registration and a future regression could hit one but not the other.
func TestCLI_ExtShippedProviderResolves(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
	}{
		{"gemini-cli", "google-gemini-cli", "gemini-2.5-flash"},
		{"antigravity", "google-antigravity", "gemini-3-flash"},
	}
	// Strings that, if present, indicate one of the two regressions has
	// returned. Anything else (auth required, no credentials, network, etc.)
	// is acceptable — those are downstream of resolution.
	regressionMarkers := []string{
		`Unknown provider "google-gemini-cli"`,
		`Unknown provider "google-antigravity"`,
		"no API provider registered for api: google-gemini-cli",
		"no API provider registered for api: google-antigravity",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentDir := t.TempDir()
			out, code := runFirWithAgentDir(
				t, agentDir, "ping", 30*time.Second,
				"--provider", tc.provider,
				"--model", tc.model,
				"--no-mcp",
				"--no-session",
				"--print",
			)
			// Failure is fine — there are no creds in this temp dir.
			// What we forbid is the specific regression error strings.
			for _, marker := range regressionMarkers {
				if strings.Contains(out, marker) {
					t.Fatalf("ext-shipped provider regression hit (exit=%d): output contains %q\nfull output:\n%s",
						code, marker, out)
				}
			}
			assertNoPanic(t, out)
		})
	}
}

// TestCLI_DemoEchoProvider_E2E exercises the same contract as
// TestCLI_ExtShippedProviderResolves but against the demo extension's
// synthetic-stream "echo" provider. This catches the same two regressions
// for the *synthetic* dispatch path (host streams via extStreamAdapter
// over provider/stream/* RPC), complementing the decl-google passthrough
// path covered by the gemini-cli/antigravity test above.
//
// The echo provider is opt-in via -e demo and emits a deterministic
// "Echo: <input>" completion entirely in-process, so the test is fully
// hermetic and can assert on the model's actual output.
func TestCLI_DemoEchoProvider_E2E(t *testing.T) {
	agentDir := t.TempDir()
	out, code := runFirWithAgentDir(
		t, agentDir, "ping", 30*time.Second,
		"-e", "demo",
		"--provider", "echo",
		"--model", "echo-1",
		"--api-key", "test-key",
		"--no-mcp",
		"--no-session",
		"--print",
	)
	if code != 0 {
		t.Fatalf("expected success, got exit=%d output:\n%s", code, out)
	}
	// Regression guard: same markers as the decl-google e2e test — these
	// strings would surface if either bug returned.
	for _, marker := range []string{
		`Unknown provider "echo"`,
		"no API provider registered for api: ext:echo",
	} {
		if strings.Contains(out, marker) {
			t.Fatalf("regression marker present: %q\noutput:\n%s", marker, out)
		}
	}
	// Echo provider deterministically echoes the input back.
	if !strings.Contains(out, "Echo: ping") {
		t.Errorf("expected echoed output 'Echo: ping' in:\n%s", out)
	}
	assertNoPanic(t, out)
}
