package print

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// mockStreamFn returns a StreamFn that produces a canned response.
func mockStreamFn(text string) agent.StreamFn {
	return func(model *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: text}}},
				Api:        model.Api,
				Provider:   model.Provider,
				Model:      model.ID,
				Usage:      ai.Usage{Input: 10, Output: 5},
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
			stream.End(nil)
		}()
		return stream
	}
}

// TestPrintMode_EndToEnd tests the full pipeline: prompt → agent → mock stream → output.
// This is the integration test for the "echo Hello | fir -p" milestone.
func TestPrintMode_EndToEnd(t *testing.T) {
	model := &ai.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Api:           "test-api",
		Provider:      "test-provider",
		BaseURL:       "http://localhost",
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText},
		ContextWindow: 200000,
		MaxTokens:     8192,
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "You are a helpful assistant.",
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: mockStreamFn("Hello! How can I help you?"),
		GetApiKey: func(provider string) (string, error) {
			return "test-api-key", nil
		},
	})

	tmpDir := t.TempDir()
	sessionMgr := store.InMemorySessionStore()
	settingsMgr := config.NewSettingsManager(tmpDir, tmpDir)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:             tmpDir,
		AgentDir:        tmpDir,
		SettingsManager: settingsMgr,
	})
	_ = rl.Reload()

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sessionMgr,
		SettingsManager: settingsMgr,
		ResourceLoader:  rl,
		Cwd:             tmpDir,
	})
	defer session.Close()

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := Run(session, Options{
		Mode:           ModeText,
		InitialMessage: "Hello",
	})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if err != nil {
		t.Fatalf("print mode returned error: %v", err)
	}

	expected := "Hello! How can I help you?"
	if !strings.Contains(output, expected) {
		t.Errorf("expected output to contain %q, got %q", expected, output)
	}
}

// TestPrintMode_EndToEnd_MultipleMessages tests sending multiple messages.
func TestPrintMode_EndToEnd_MultipleMessages(t *testing.T) {
	callCount := 0
	responses := []string{"First response", "Second response"}

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
			idx := callCount
			if idx >= len(responses) {
				idx = len(responses) - 1
			}
			callCount++
			return mockStreamFn(responses[idx])(m, ctx, opts)
		},
		GetApiKey: func(provider string) (string, error) {
			return "test-api-key", nil
		},
	})

	tmpDir := t.TempDir()
	sessionMgr := store.InMemorySessionStore()
	settingsMgr := config.NewSettingsManager(tmpDir, tmpDir)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:             tmpDir,
		AgentDir:        tmpDir,
		SettingsManager: settingsMgr,
	})
	_ = rl.Reload()

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sessionMgr,
		SettingsManager: settingsMgr,
		ResourceLoader:  rl,
		Cwd:             tmpDir,
	})
	defer session.Close()

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := Run(session, Options{
		Mode:           ModeText,
		InitialMessage: "First question",
		Messages:       []string{"Second question"},
	})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if err != nil {
		t.Fatalf("print mode returned error: %v", err)
	}

	// Text mode outputs only the last response
	if !strings.Contains(output, "Second response") {
		t.Errorf("expected output to contain 'Second response', got %q", output)
	}
}

// TestPrintMode_EndToEnd_JSON tests JSON output mode.
func TestPrintMode_EndToEnd_JSON(t *testing.T) {
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
		StreamFn: mockStreamFn("JSON test response"),
		GetApiKey: func(provider string) (string, error) {
			return "test-api-key", nil
		},
	})

	tmpDir := t.TempDir()
	sessionMgr := store.InMemorySessionStore()
	settingsMgr := config.NewSettingsManager(tmpDir, tmpDir)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:             tmpDir,
		AgentDir:        tmpDir,
		SettingsManager: settingsMgr,
	})
	_ = rl.Reload()

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sessionMgr,
		SettingsManager: settingsMgr,
		ResourceLoader:  rl,
		Cwd:             tmpDir,
	})
	defer session.Close()

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := Run(session, Options{
		Mode:           ModeJSON,
		InitialMessage: "Hello",
	})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if err != nil {
		t.Fatalf("print mode returned error: %v", err)
	}

	// JSON mode should output JSON lines with event data
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		t.Fatal("expected JSON output lines, got none")
	}

	// Each line should start with '{'
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			t.Errorf("line %d is not JSON: %s", i, line)
		}
	}
}

// TestPrintMode_NoMessage tests running with no initial message.
func TestPrintMode_NoMessage(t *testing.T) {
	model := &ai.Model{
		ID:            "test-model",
		Api:           "test-api",
		Provider:      "test-provider",
		ContextWindow: 200000,
		MaxTokens:     8192,
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: mockStreamFn("should not be called"),
		GetApiKey: func(provider string) (string, error) {
			return "test-api-key", nil
		},
	})

	tmpDir := t.TempDir()
	sessionMgr := store.InMemorySessionStore()
	settingsMgr := config.NewSettingsManager(tmpDir, tmpDir)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:             tmpDir,
		AgentDir:        tmpDir,
		SettingsManager: settingsMgr,
	})
	_ = rl.Reload()

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sessionMgr,
		SettingsManager: settingsMgr,
		ResourceLoader:  rl,
		Cwd:             tmpDir,
	})
	defer session.Close()

	err := Run(session, Options{
		Mode: ModeText,
		// No InitialMessage
	})

	if err != nil {
		t.Fatalf("expected no error with empty message, got: %v", err)
	}
}
