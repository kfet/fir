package compaction

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
)

func TestDefaultRunner_IsEnabled(t *testing.T) {
	// Default settings (nil Compaction block) → enabled=true
	sm := core.NewInMemorySettingsManager(core.Settings{})
	runner := &DefaultRunner{SettingsManager: sm}
	if !runner.IsEnabled() {
		t.Error("expected IsEnabled()=true with default settings")
	}

	// Explicitly disabled
	disabled := false
	smOff := core.NewInMemorySettingsManager(core.Settings{
		Compaction: &core.CompactionSettings{
			Enabled: &disabled,
		},
	})
	runnerOff := &DefaultRunner{SettingsManager: smOff}
	if runnerOff.IsEnabled() {
		t.Error("expected IsEnabled()=false when Enabled=false")
	}

	// Explicitly enabled
	enabled := true
	smOn := core.NewInMemorySettingsManager(core.Settings{
		Compaction: &core.CompactionSettings{
			Enabled: &enabled,
		},
	})
	runnerOn := &DefaultRunner{SettingsManager: smOn}
	if !runnerOn.IsEnabled() {
		t.Error("expected IsEnabled()=true when Enabled=true")
	}
}

func TestDefaultRunner_ShouldCompact(t *testing.T) {
	sm := core.NewInMemorySettingsManager(core.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
	}

	// Default settings: reserveTokens=16384
	// Should compact when contextTokens > contextWindow - reserveTokens
	if runner.ShouldCompact(50000, 200000) {
		t.Error("should not compact when well under threshold")
	}

	// contextWindow=200000, reserve=16384 → threshold=183616
	if !runner.ShouldCompact(190000, 200000) {
		t.Error("should compact when over threshold")
	}
}

func TestDefaultRunner_ShouldCompact_Disabled(t *testing.T) {
	enabled := false
	sm := core.NewInMemorySettingsManager(core.Settings{
		Compaction: &core.CompactionSettings{
			Enabled: &enabled,
		},
	})
	runner := &DefaultRunner{
		SettingsManager: sm,
	}

	if runner.ShouldCompact(190000, 200000) {
		t.Error("should not compact when disabled")
	}
}

// noopResourceLoader satisfies the ResourceLoader interface for tests.
type noopResourceLoader struct{}

func (n noopResourceLoader) GetSkills() ([]core.Skill, []core.ResourceDiagnostic) { return nil, nil }
func (n noopResourceLoader) GetPrompts() ([]core.PromptTemplate, []core.ResourceDiagnostic) {
	return nil, nil
}
func (n noopResourceLoader) GetAgentsFiles() []core.AgentsFile           { return nil }
func (n noopResourceLoader) GetSystemPrompt() string                     { return "" }
func (n noopResourceLoader) GetAppendSystemPrompt() []string             { return nil }
func (n noopResourceLoader) GetPathMetadata() map[string]core.PathMetadata { return nil }
func (n noopResourceLoader) ExtendResources(core.ResourceExtensionPaths) {}
func (n noopResourceLoader) Reload() error                               { return nil }

// makeTestSession creates a minimal AgentSession for testing RunCompaction error paths.
func makeTestSession(t *testing.T, model *ai.Model) *core.AgentSession {
	t.Helper()
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "session")
	os.MkdirAll(sessionDir, 0o755)

	ag := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model: model,
		},
	})
	smgr := core.NewSessionManager(tmpDir, sessionDir)

	return core.NewAgentSession(core.AgentSessionOptions{
		Agent:          ag,
		SessionManager: smgr,
		SettingsManager: core.NewInMemorySettingsManager(core.Settings{}),
		ResourceLoader: noopResourceLoader{},
		Cwd:            tmpDir,
	})
}

func TestRunCompaction_NoModel(t *testing.T) {
	session := makeTestSession(t, nil)
	sm := core.NewInMemorySettingsManager(core.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
		ModelRegistry:   core.NewModelRegistry(core.NewInMemoryAuthStorage(nil), ""),
	}
	_, err := runner.RunCompaction(context.Background(), session, "")
	if err == nil {
		t.Fatal("expected error when no model")
	}
	if !strings.Contains(err.Error(), "no model") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunCompaction_NoApiKey(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		Provider: "test-provider",
	}
	session := makeTestSession(t, model)
	sm := core.NewInMemorySettingsManager(core.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
		ModelRegistry:   core.NewModelRegistry(core.NewInMemoryAuthStorage(nil), ""),
	}
	_, err := runner.RunCompaction(context.Background(), session, "")
	if err == nil {
		t.Fatal("expected error when no API key")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunCompaction_NothingToCompact(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		Provider: "test-provider",
	}
	session := makeTestSession(t, model)
	sm := core.NewInMemorySettingsManager(core.Settings{})

	// Create a registry with a pre-seeded API key (in-memory, no disk I/O).
	authStorage := core.NewInMemoryAuthStorage(core.AuthStorageData{
		"test-provider": {Type: core.CredentialTypeAPIKey, Key: "test-key-123"},
	})
	registry := core.NewModelRegistry(authStorage, "")

	runner := &DefaultRunner{
		SettingsManager: sm,
		ModelRegistry:   registry,
	}
	_, err := runner.RunCompaction(context.Background(), session, "")
	if err == nil {
		t.Fatal("expected error when nothing to compact")
	}
	if !strings.Contains(err.Error(), "nothing to compact") && !strings.Contains(err.Error(), "already compacted") {
		t.Errorf("unexpected error: %v", err)
	}
}
