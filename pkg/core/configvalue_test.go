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
	os.Setenv("PI_TEST_CONFIG_VALUE", "my-secret-key")
	defer os.Unsetenv("PI_TEST_CONFIG_VALUE")

	result := ResolveConfigValue("PI_TEST_CONFIG_VALUE")
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
	os.Setenv("PI_TEST_HEADER_VAL", "resolved-header")
	defer os.Unsetenv("PI_TEST_HEADER_VAL")

	headers := map[string]string{
		"X-Custom":  "literal-value",
		"X-FromEnv": "PI_TEST_HEADER_VAL",
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
