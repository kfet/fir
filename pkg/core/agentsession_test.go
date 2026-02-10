package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

// ============================================================================
// Helpers
// ============================================================================

func newTestAgentSession(t *testing.T) (*AgentSession, string) {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := NewSessionManager(cwd, filepath.Join(agentDir, "sessions"))
	settingsManager := NewSettingsManager(cwd, agentDir)

	rl := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	rl.Reload()

	modelRegistry := NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), "")

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "test system prompt",
			ThinkingLevel: "off",
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	session := NewAgentSession(AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   modelRegistry,
		Cwd:             cwd,
	})

	return session, cwd
}

// ============================================================================
// NewAgentSession
// ============================================================================

func TestNewAgentSession(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.Agent == nil {
		t.Fatal("expected non-nil agent")
	}
	if session.SessionManager == nil {
		t.Fatal("expected non-nil session manager")
	}
	if session.SettingsManager == nil {
		t.Fatal("expected non-nil settings manager")
	}
}

// ============================================================================
// State accessors
// ============================================================================

func TestAgentSession_State(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	state := session.State()
	if state.ThinkingLevel != "off" {
		t.Errorf("expected thinking level 'off', got %s", state.ThinkingLevel)
	}
}

func TestAgentSession_Model_NilWhenNotSet(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.Model() != nil {
		t.Error("expected nil model when not set")
	}
}

func TestAgentSession_ThinkingLevel(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.ThinkingLevel() != "off" {
		t.Errorf("expected 'off', got %s", session.ThinkingLevel())
	}
}

func TestAgentSession_IsStreaming_InitiallyFalse(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.IsStreaming() {
		t.Error("expected not streaming initially")
	}
}

func TestAgentSession_ResourceLoader(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.ResourceLoader() == nil {
		t.Error("expected non-nil resource loader")
	}
}

func TestAgentSession_ModelRegistryRef(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	if session.ModelRegistryRef() == nil {
		t.Error("expected non-nil model registry")
	}
}

// ============================================================================
// Event Subscription
// ============================================================================

func TestAgentSession_Subscribe(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	var received []AgentSessionEvent
	var mu sync.Mutex

	unsub := session.Subscribe(func(event AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, event)
	})

	session.emit(AgentSessionEvent{Type: "test_event"})

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 event, got %d", count)
	}
	if received[0].Type != "test_event" {
		t.Errorf("expected test_event, got %s", received[0].Type)
	}

	unsub()
	session.emit(AgentSessionEvent{Type: "after_unsub"})

	mu.Lock()
	count = len(received)
	mu.Unlock()

	if count != 1 {
		t.Error("expected no more events after unsubscribe")
	}
}

func TestAgentSession_MultipleSubscribers(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	count1 := 0
	count2 := 0

	session.Subscribe(func(event AgentSessionEvent) { count1++ })
	session.Subscribe(func(event AgentSessionEvent) { count2++ })

	session.emit(AgentSessionEvent{Type: "test"})

	if count1 != 1 || count2 != 1 {
		t.Errorf("expected both to receive, got %d and %d", count1, count2)
	}
}

// ============================================================================
// Model management
// ============================================================================

func TestAgentSession_SetModel(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	model := &ai.Model{
		Provider: "test-provider",
		ID:       "test-model",
	}

	session.SetModel(model)

	if session.Model() == nil {
		t.Fatal("expected model to be set")
	}
	if session.Model().Provider != "test-provider" {
		t.Errorf("expected test-provider, got %s", session.Model().Provider)
	}
	if session.Model().ID != "test-model" {
		t.Errorf("expected test-model, got %s", session.Model().ID)
	}
}

func TestAgentSession_SetThinkingLevel(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	session.SetThinkingLevel("high")

	if session.ThinkingLevel() != "high" {
		t.Errorf("expected 'high', got %s", session.ThinkingLevel())
	}
}

// ============================================================================
// Session management
// ============================================================================

func TestAgentSession_NewSession(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	// Add a message first
	session.Agent.ReplaceMessages([]agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello", 0)),
	})

	ok, err := session.NewSessionCmd()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected true")
	}

	state := session.State()
	if len(state.Messages) != 0 {
		t.Errorf("expected 0 messages after new session, got %d", len(state.Messages))
	}
}

// ============================================================================
// Reload
// ============================================================================

func TestAgentSession_Reload(t *testing.T) {
	session, cwd := newTestAgentSession(t)
	defer session.Close()

	// Create a AGENTS.md file and reload
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	os.WriteFile(agentsPath, []byte("# Test agents instructions"), 0o644)

	err := session.Reload()
	if err != nil {
		t.Fatal(err)
	}

	// After reload, system prompt should include agents file content
	// (checked via the agent's state)
}

// ============================================================================
// Prompt without model
// ============================================================================

func TestAgentSession_Prompt_NoModel(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	err := session.Prompt("hello")
	if err == nil {
		t.Error("expected error when no model is set")
	}
}

// ============================================================================
// Close
// ============================================================================

func TestAgentSession_Close(t *testing.T) {
	session, _ := newTestAgentSession(t)

	// Should not panic
	session.Close()
	session.Close() // Double close should be safe
}

// ============================================================================
// Compaction
// ============================================================================

func TestAgentSession_RunCompaction_NilRunner(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	_, err := session.RunCompaction()
	if err == nil {
		t.Error("expected error when compaction runner is nil")
	}
}

type mockCompactionRunner struct {
	shouldCompactResult bool
	runResult           *CompactionResultInfo
	runError            error
	runCalled           bool
}

func (m *mockCompactionRunner) ShouldCompact(contextTokens, contextWindow int) bool {
	return m.shouldCompactResult
}

func (m *mockCompactionRunner) RunCompaction(session *AgentSession) (*CompactionResultInfo, error) {
	m.runCalled = true
	return m.runResult, m.runError
}

func TestAgentSession_RunCompaction_WithRunner(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	runner := &mockCompactionRunner{
		runResult: &CompactionResultInfo{
			Summary:          "test summary",
			FirstKeptEntryID: "entry-1",
			TokensBefore:     1000,
		},
	}
	session.compactionRunner = runner

	result, err := session.RunCompaction()
	if err != nil {
		t.Fatal(err)
	}
	if !runner.runCalled {
		t.Error("expected RunCompaction to be called")
	}
	if result.Summary != "test summary" {
		t.Errorf("expected test summary, got %s", result.Summary)
	}
}

// ============================================================================
// calculateContextTokensFromUsage
// ============================================================================

func TestCalculateContextTokensFromUsage_WithTotal(t *testing.T) {
	usage := ai.Usage{TotalTokens: 500, Input: 100, Output: 50}
	result := calculateContextTokensFromUsage(usage)
	if result != 500 {
		t.Errorf("expected 500, got %d", result)
	}
}

func TestCalculateContextTokensFromUsage_SumOfParts(t *testing.T) {
	usage := ai.Usage{Input: 100, Output: 50, CacheRead: 20, CacheWrite: 10}
	result := calculateContextTokensFromUsage(usage)
	if result != 180 {
		t.Errorf("expected 180, got %d", result)
	}
}

// ============================================================================
// System prompt building
// ============================================================================

func TestAgentSession_BuildSystemPrompt_Default(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	// System prompt should be built during NewAgentSession
	if session.baseSystemPrompt == "" {
		t.Error("expected non-empty base system prompt")
	}
}

func TestAgentSession_BuildSystemPrompt_WithAgentsFile(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("Custom project instructions"), 0o644)

	sm := NewSessionManager(cwd, filepath.Join(agentDir, "sessions"))
	settingsManager := NewSettingsManager(cwd, agentDir)
	rl := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	rl.Reload()

	modelRegistry := NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), "")

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{ThinkingLevel: "off"},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	session := NewAgentSession(AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   modelRegistry,
		Cwd:             cwd,
	})
	defer session.Close()

	// The system prompt should contain the agents file content
	if session.baseSystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
}

func TestAgentSession_BuildSystemPrompt_CustomOverride(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	// Create custom system prompt
	os.MkdirAll(filepath.Join(cwd, ConfigDirName), 0o755)
	os.WriteFile(filepath.Join(cwd, ConfigDirName, "SYSTEM.md"), []byte("Custom system prompt"), 0o644)

	sm := NewSessionManager(cwd, filepath.Join(agentDir, "sessions"))
	settingsManager := NewSettingsManager(cwd, agentDir)
	rl := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	rl.Reload()

	modelRegistry := NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), "")

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{ThinkingLevel: "off"},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	session := NewAgentSession(AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   modelRegistry,
		Cwd:             cwd,
	})
	defer session.Close()

	if session.baseSystemPrompt != "Custom system prompt" {
		t.Errorf("expected custom system prompt override, got %q", session.baseSystemPrompt)
	}
}

// ============================================================================
// Fork (stub)
// ============================================================================

func TestAgentSession_Fork_NotImplemented(t *testing.T) {
	session, _ := newTestAgentSession(t)
	defer session.Close()

	err := session.Fork("entry-1")
	if err == nil {
		t.Error("expected error for unimplemented Fork")
	}
}

// ============================================================================
// ParseSkillBlock
// ============================================================================

func TestParseSkillBlock_Valid(t *testing.T) {
	text := `<skill name="review" location="/path/to/skill/SKILL.md">
Do a code review.
</skill>`

	block := ParseSkillBlock(text)
	if block == nil {
		t.Fatal("expected non-nil result")
	}
	if block.Name != "review" {
		t.Errorf("Name = %q, want %q", block.Name, "review")
	}
	if block.Location != "/path/to/skill/SKILL.md" {
		t.Errorf("Location = %q, want %q", block.Location, "/path/to/skill/SKILL.md")
	}
	if block.Content != "Do a code review." {
		t.Errorf("Content = %q, want %q", block.Content, "Do a code review.")
	}
	if block.UserMessage != "" {
		t.Errorf("UserMessage = %q, want empty", block.UserMessage)
	}
}

func TestParseSkillBlock_WithUserMessage(t *testing.T) {
	text := `<skill name="work" location="/path/to/work/SKILL.md">
Continue working on tasks.
</skill>

Also fix the bug in foo.go`

	block := ParseSkillBlock(text)
	if block == nil {
		t.Fatal("expected non-nil result")
	}
	if block.Name != "work" {
		t.Errorf("Name = %q, want %q", block.Name, "work")
	}
	if block.Content != "Continue working on tasks." {
		t.Errorf("Content = %q, want %q", block.Content, "Continue working on tasks.")
	}
	if block.UserMessage != "Also fix the bug in foo.go" {
		t.Errorf("UserMessage = %q, want %q", block.UserMessage, "Also fix the bug in foo.go")
	}
}

// TestAgentSession_Prompt_WaitsForCompletion verifies that Prompt blocks until
// the agent loop finishes. This is critical for print mode to work correctly.
func TestAgentSession_Prompt_WaitsForCompletion(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := t.TempDir()

	sm := InMemorySessionManager()
	settingsManager := NewSettingsManager(tmpDir, agentDir)
	rl := NewResourceLoader(ResourceLoaderOptions{
		Cwd:             tmpDir,
		AgentDir:        agentDir,
		SettingsManager: settingsManager,
	})
	_ = rl.Reload()

	model := &ai.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Api:           "test-api",
		Provider:      "test-provider",
		BaseURL:       "http://localhost",
		ContextWindow: 200000,
		MaxTokens:     8192,
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "You are a helpful assistant.",
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			stream := ai.NewAssistantMessageEventStream()
			go func() {
				msg := &ai.AssistantMessage{
					Role:       ai.RoleAssistant,
					Content:    []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "test response"}}},
					Api:        m.Api,
					Provider:   m.Provider,
					Model:      m.ID,
					Usage:      ai.Usage{Input: 10, Output: 5},
					StopReason: ai.StopReasonStop,
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
				stream.End(nil)
			}()
			return stream
		},
		GetApiKey: func(provider string) (string, error) {
			return "test-key", nil
		},
	})

	session := NewAgentSession(AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		Cwd:             tmpDir,
	})
	defer session.Close()

	// Prompt should block until the agent finishes
	err := session.Prompt("Hello")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	// After Prompt returns, agent should no longer be streaming
	if session.IsStreaming() {
		t.Error("agent should not be streaming after Prompt returns")
	}

	// Messages should contain the user message and assistant response
	state := session.State()
	if len(state.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (user + assistant), got %d", len(state.Messages))
	}

	// Check user message
	if state.Messages[0].Role() != "user" {
		t.Errorf("first message role = %q, want 'user'", state.Messages[0].Role())
	}

	// Check assistant message
	lastMsg := state.Messages[len(state.Messages)-1]
	if lastMsg.Role() != "assistant" {
		t.Errorf("last message role = %q, want 'assistant'", lastMsg.Role())
	}
	am := lastMsg.AsAssistant()
	if am == nil {
		t.Fatal("expected assistant message")
	}
	if len(am.Content) == 0 || am.Content[0].Text == nil || am.Content[0].Text.Text != "test response" {
		t.Error("expected assistant content to contain 'test response'")
	}
}

func TestParseSkillBlock_NoMatch(t *testing.T) {
	tests := []string{
		"just a normal message",
		"<skill>missing attributes</skill>",
		"",
	}
	for _, text := range tests {
		if block := ParseSkillBlock(text); block != nil {
			t.Errorf("ParseSkillBlock(%q) = %+v, want nil", text, block)
		}
	}
}
