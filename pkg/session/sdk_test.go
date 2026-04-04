package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/resources"
)

// ============================================================================
// DefaultAgentDir
// ============================================================================

func TestDefaultAgentDir(t *testing.T) {
	// Unset XDG_CONFIG_HOME to test the default path.
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := DefaultAgentDir()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "fir")
	if dir != expected {
		t.Errorf("expected %s, got %s", expected, dir)
	}
}

func TestDefaultAgentDirXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	dir := DefaultAgentDir()
	expected := filepath.Join("/custom/config", "fir")
	if dir != expected {
		t.Errorf("expected %s, got %s", expected, dir)
	}
}

// ============================================================================
// DefaultCodingTools
// ============================================================================

func TestDefaultCodingTools(t *testing.T) {
	tools := DefaultCodingTools("/tmp/test")
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	expected := []string{"read", "bash", "edit", "write"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected tool %s, got names: %v", name, names)
		}
	}
}

// ============================================================================
// AllTools
// ============================================================================

func TestAllTools(t *testing.T) {
	tools := AllTools("/tmp/test")
	if len(tools) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	expected := []string{"read", "bash", "edit", "write", "grep", "find", "ls"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected tool %s, got names: %v", name, names)
		}
	}
}

// ============================================================================
// CreateAgentSession - minimal
// ============================================================================

func TestCreateAgentSession_Minimal(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.Session == nil {
		t.Fatal("expected non-nil session")
	}

	// No API keys, so model should be nil and we should get a fallback message
	if result.ModelFallbackMessage == "" {
		t.Log("Note: model fallback message was empty (may have found a model via env key)")
	}
}

func TestCreateAgentSession_WithCustomTools(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	customTools := []agent.AgentTool{
		// Just use Read as a stand-in
	}

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
		Tools:    customTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestCreateAgentSession_WithResourceLoader(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	rl.Reload()

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:            cwd,
		AgentDir:       agentDir,
		ResourceLoader: rl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestCreateAgentSession_WithSettingsManager(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := config.NewSettingsManager(cwd, agentDir)

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		SettingsManager: sm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestCreateAgentSession_ThinkingLevelDefaults(t *testing.T) {
	// Isolate from any real API keys in the environment so no model is picked
	// as available; without a model the thinking level must be "off".
	for _, key := range envkeys.KnownApiKeyEnvVars() {
		t.Setenv(key, "")
	}
	cwd := t.TempDir()
	agentDir := t.TempDir()

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Without a model that supports reasoning, thinking should be "off"
	state := result.Session.Agent.State()
	if state.ThinkingLevel != "off" {
		t.Errorf("expected thinking level 'off' without reasoning model, got %s", state.ThinkingLevel)
	}
}

func TestCreateAgentSession_DefaultCwd(t *testing.T) {
	agentDir := t.TempDir()

	result, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		AgentDir: agentDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == nil {
		t.Fatal("expected non-nil session")
	}
}

// ============================================================================
// CreateAgentSessionOptions defaults
// ============================================================================

func TestCreateAgentSessionOptions_Defaults(t *testing.T) {
	opts := CreateAgentSessionOptions{}
	if opts.Cwd != "" {
		t.Error("expected empty Cwd default")
	}
	if opts.AgentDir != "" {
		t.Error("expected empty AgentDir default")
	}
	if opts.Model != nil {
		t.Error("expected nil Model default")
	}
}

func TestResolveServerTools_ShortNames(t *testing.T) {
	tools := ResolveServerTools([]string{"web_search", "web_fetch", "code_execution"})
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	if tools[0].Type != "web_search_20250305" {
		t.Errorf("expected web_search_20250305, got %s", tools[0].Type)
	}
	if tools[1].Type != "web_fetch_20250910" {
		t.Errorf("expected web_fetch_20250910, got %s", tools[1].Type)
	}
	if tools[2].Type != "code_execution_20250825" {
		t.Errorf("expected code_execution_20250825, got %s", tools[2].Type)
	}
}

func TestResolveServerTools_RawTypePassthrough(t *testing.T) {
	tools := ResolveServerTools([]string{"web_search_20260209"})
	if len(tools) != 1 || tools[0].Type != "web_search_20260209" {
		t.Errorf("expected raw type passthrough, got %v", tools)
	}
}

func TestResolveServerTools_DeduplicateCodeExecution(t *testing.T) {
	// Dynamic filtering versions auto-inject code_execution, so explicit ones should be removed
	tools := ResolveServerTools([]string{"web_search_20260209", "code_execution"})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (code_execution removed), got %d: %v", len(tools), tools)
	}
	if tools[0].Type != "web_search_20260209" {
		t.Errorf("expected web_search_20260209, got %s", tools[0].Type)
	}
}

func TestResolveServerTools_NoDeduplicateWithBasicVersions(t *testing.T) {
	// Basic versions don't auto-inject, so keep code_execution
	tools := ResolveServerTools([]string{"web_search", "code_execution"})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}
