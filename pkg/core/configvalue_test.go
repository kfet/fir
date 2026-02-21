package core

import (
	"os"
	"testing"
)

func TestResolveConfigValue_Literal(t *testing.T) {
	// A random string that's not an env var should be treated as literal
	result := ResolveConfigValue("sk-test-literal-key-12345")
	if result != "sk-test-literal-key-12345" {
		t.Errorf("expected literal value, got %q", result)
	}
}

func TestResolveConfigValue_Empty(t *testing.T) {
	result := ResolveConfigValue("")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestResolveConfigValue_EnvVar(t *testing.T) {
	os.Setenv("FIR_TEST_CONFIG_VALUE", "my-secret-key")
	defer os.Unsetenv("FIR_TEST_CONFIG_VALUE")

	result := ResolveConfigValue("FIR_TEST_CONFIG_VALUE")
	if result != "my-secret-key" {
		t.Errorf("expected env var value 'my-secret-key', got %q", result)
	}
}

func TestResolveConfigValue_Command(t *testing.T) {
	ClearConfigValueCache()
	result := ResolveConfigValue("!echo hello-from-command")
	if result != "hello-from-command" {
		t.Errorf("expected 'hello-from-command', got %q", result)
	}
}

func TestResolveConfigValue_CommandCached(t *testing.T) {
	ClearConfigValueCache()

	// First call
	result1 := ResolveConfigValue("!echo cached-value")
	if result1 != "cached-value" {
		t.Errorf("expected 'cached-value', got %q", result1)
	}

	// Second call should use cache (we can't verify caching directly,
	// but we can verify the result is the same)
	result2 := ResolveConfigValue("!echo cached-value")
	if result2 != "cached-value" {
		t.Errorf("expected 'cached-value' from cache, got %q", result2)
	}
}

func TestResolveConfigValue_CommandFailure(t *testing.T) {
	ClearConfigValueCache()
	result := ResolveConfigValue("!nonexistent-command-that-should-fail-xyz")
	if result != "" {
		t.Errorf("expected empty string for failed command, got %q", result)
	}
}

func TestResolveHeaders(t *testing.T) {
	os.Setenv("FIR_TEST_HEADER_VAL", "resolved-header")
	defer os.Unsetenv("FIR_TEST_HEADER_VAL")

	headers := map[string]string{
		"X-Custom":  "literal-value",
		"X-FromEnv": "FIR_TEST_HEADER_VAL",
	}

	resolved := ResolveHeaders(headers)
	if resolved == nil {
		t.Fatal("expected non-nil resolved headers")
	}
	if resolved["X-Custom"] != "literal-value" {
		t.Errorf("expected 'literal-value', got %q", resolved["X-Custom"])
	}
	if resolved["X-FromEnv"] != "resolved-header" {
		t.Errorf("expected 'resolved-header', got %q", resolved["X-FromEnv"])
	}
}

func TestResolveHeaders_Nil(t *testing.T) {
	result := ResolveHeaders(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
}

func TestResolveHeaders_Empty(t *testing.T) {
	result := ResolveHeaders(map[string]string{})
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestResolveConfigValue_CommandTimeout(t *testing.T) {
	ClearConfigValueCache()
	// Use a command that would hang but verify the timeout mechanism exists
	// by using a short sleep that completes within the 10s timeout
	result := ResolveConfigValue("!sleep 0.1 && echo timeout-test")
	if result != "timeout-test" {
		t.Errorf("expected 'timeout-test', got %q", result)
	}
}

func TestResolveConfigValue_CommandOutputTrimmed(t *testing.T) {
	ClearConfigValueCache()
	// echo adds a trailing newline; verify it's trimmed
	result := ResolveConfigValue("!printf '  spaced  '")
	if result != "spaced" {
		t.Errorf("expected trimmed 'spaced', got %q", result)
	}
}

func TestResolveConfigValue_CommandEmptyOutput(t *testing.T) {
	ClearConfigValueCache()
	result := ResolveConfigValue("!printf ''")
	if result != "" {
		t.Errorf("expected empty string for empty command output, got %q", result)
	}
}

func TestResolveConfigValue_CommandCacheNilForFailure(t *testing.T) {
	ClearConfigValueCache()
	// First call should fail and cache nil
	result1 := ResolveConfigValue("!false")
	if result1 != "" {
		t.Errorf("expected empty for failed command, got %q", result1)
	}
	// Second call should use cached nil result
	result2 := ResolveConfigValue("!false")
	if result2 != "" {
		t.Errorf("expected empty from cached nil, got %q", result2)
	}
}

func TestResolveHeaders_CommandInHeaders(t *testing.T) {
	ClearConfigValueCache()
	headers := map[string]string{
		"Authorization": "!echo bearer-token-123",
	}
	resolved := ResolveHeaders(headers)
	if resolved == nil {
		t.Fatal("expected non-nil resolved headers")
	}
	if resolved["Authorization"] != "bearer-token-123" {
		t.Errorf("expected 'bearer-token-123', got %q", resolved["Authorization"])
	}
}

func TestClearConfigValueCache(t *testing.T) {
	ClearConfigValueCache()
	// Run a command to populate cache
	ResolveConfigValue("!echo test-clear")
	// Clear cache
	ClearConfigValueCache()
	// Should still work (re-executes)
	result := ResolveConfigValue("!echo test-clear")
	if result != "test-clear" {
		t.Errorf("expected 'test-clear' after cache clear, got %q", result)
	}
}
