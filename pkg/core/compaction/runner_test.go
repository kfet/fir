package compaction

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/auth"
)

func TestDefaultRunner_IsEnabled(t *testing.T) {
	// Default settings (nil Compaction block) → enabled=true
	sm := config.NewInMemorySettingsManager(config.Settings{})
	runner := &DefaultRunner{SettingsManager: sm}
	if !runner.IsEnabled() {
		t.Error("expected IsEnabled()=true with default settings")
	}

	// Explicitly disabled
	disabled := false
	smOff := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			Enabled: &disabled,
		},
	})
	runnerOff := &DefaultRunner{SettingsManager: smOff}
	if runnerOff.IsEnabled() {
		t.Error("expected IsEnabled()=false when Enabled=false")
	}

	// Explicitly enabled
	enabled := true
	smOn := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			Enabled: &enabled,
		},
	})
	runnerOn := &DefaultRunner{SettingsManager: smOn}
	if !runnerOn.IsEnabled() {
		t.Error("expected IsEnabled()=true when Enabled=true")
	}
}

func TestDefaultRunner_ShouldCompact(t *testing.T) {
	sm := config.NewInMemorySettingsManager(config.Settings{})
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
	sm := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
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

func TestDefaultRunner_GetStats_EmptySession(t *testing.T) {
	session := makeTestSession(t, nil)
	sm := config.NewInMemorySettingsManager(config.Settings{})
	runner := &DefaultRunner{SettingsManager: sm}

	info := runner.GetStats(session)
	if info != nil {
		t.Errorf("expected nil from empty session, got %+v", info)
	}
}

func TestDefaultRunner_GetStats_WithMessages(t *testing.T) {
	session := makeTestSession(t, nil)

	// Add a user → assistant exchange with real usage data so that
	// EstimateContextTokens uses the production (usage-based) path.
	session.SessionManager.AppendAIMessage(ai.NewUserMsg("Hello, world!", 1000))
	session.SessionManager.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:    []ai.AssistantContent{{Text: &ai.TextContent{Text: "Hi there!"}}},
		StopReason: ai.StopReasonStop,
		Usage:      ai.Usage{Input: 10, Output: 3},
	}))

	sm := config.NewInMemorySettingsManager(config.Settings{})
	runner := &DefaultRunner{SettingsManager: sm}

	info := runner.GetStats(session)
	if info == nil {
		t.Fatal("expected non-nil CompactionInfo for session with messages")
	}
	if info.TokensBefore <= 0 {
		t.Errorf("expected TokensBefore > 0, got %d", info.TokensBefore)
	}
	// MessagesToSummarize may be 0 when all messages fit in KeepRecentTokens;
	// just verify the field is non-negative.
	if info.MessagesToSummarize < 0 {
		t.Errorf("expected MessagesToSummarize >= 0, got %d", info.MessagesToSummarize)
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
		SettingsManager: config.NewInMemorySettingsManager(config.Settings{}),
		ResourceLoader: noopResourceLoader{},
		Cwd:            tmpDir,
	})
}

func TestRunCompaction_NoModel(t *testing.T) {
	session := makeTestSession(t, nil)
	sm := config.NewInMemorySettingsManager(config.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
		ModelRegistry:   core.NewModelRegistry(auth.NewInMemoryAuthStorage(nil), ""),
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
	sm := config.NewInMemorySettingsManager(config.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
		ModelRegistry:   core.NewModelRegistry(auth.NewInMemoryAuthStorage(nil), ""),
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
	sm := config.NewInMemorySettingsManager(config.Settings{})

	// Create a registry with a pre-seeded API key (in-memory, no disk I/O).
	authStorage := auth.NewInMemoryAuthStorage(auth.AuthStorageData{
		"test-provider": {Type: auth.CredentialTypeAPIKey, Key: "test-key-123"},
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
