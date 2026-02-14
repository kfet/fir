package core

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

// TestAutoCompaction_E2E_ThresholdTriggered simulates a full agent loop
// where the provider returns a high-usage assistant message, and verifies
// that auto-compaction is triggered via the event bus.
func TestAutoCompaction_E2E_ThresholdTriggered(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := InMemorySessionManager(cwd)
	settingsManager := NewSettingsManager(cwd, agentDir)

	model := &ai.Model{
		ID:            "test-model",
		Provider:      "test-provider",
		Api:           "openai-completions",
		ContextWindow: 100000,
		MaxTokens:     4096,
	}

	// Create a fake stream function that returns a high-usage response
	fakeStreamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent("This is a response with high usage")},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage: ai.Usage{
					Input:       90000, // very close to 100k context window
					Output:      5000,
					TotalTokens: 95000,
				},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{
				Type:    ai.EventDone,
				Reason:  ai.StopReasonStop,
				Message: output,
			})
			stream.End(nil)
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return fakeStreamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	runner := &mockCompactionRunner{
		shouldCompactResult: true,
		runResult: &CompactionResultInfo{
			Summary:          "compacted via threshold",
			FirstKeptEntryID: "entry-1",
			TokensBefore:     90000,
		},
	}

	rl := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	session := NewAgentSession(AgentSessionOptions{
		Agent:            a,
		SessionManager:   sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), ""),
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer session.Close()

	// Collect events
	var events []AgentSessionEvent
	var mu sync.Mutex
	session.Subscribe(func(e AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	// Send a prompt and wait for agent to finish
	err := session.Prompt("Hello, please respond")
	if err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	// Wait for the agent to complete
	a.WaitForIdle()
	// Give a bit of time for the compaction event to propagate
	time.Sleep(100 * time.Millisecond)

	// Check events
	mu.Lock()
	defer mu.Unlock()

	t.Logf("Collected %d events:", len(events))
	for _, e := range events {
		t.Logf("  event type=%s", e.Type)
		if e.AgentEvent != nil && e.AgentEvent.Message != nil {
			if am := e.AgentEvent.Message.AsAssistant(); am != nil {
				t.Logf("    assistant: provider=%s model=%s stopReason=%s usage=%+v",
					am.Provider, am.Model, am.StopReason, am.Usage)
			}
		}
	}

	// Verify auto_compaction_start was emitted
	compactionStarted := false
	compactionEnded := false
	for _, e := range events {
		if e.Type == "auto_compaction_start" {
			compactionStarted = true
			if e.CompactionReason != "threshold" {
				t.Errorf("expected compaction reason 'threshold', got %q", e.CompactionReason)
			}
		}
		if e.Type == "auto_compaction_end" {
			compactionEnded = true
		}
	}

	if !compactionStarted {
		t.Error("auto_compaction_start event was NOT emitted — compaction did not trigger")
	}
	if !compactionEnded {
		t.Error("auto_compaction_end event was NOT emitted")
	}
	if !runner.runCalled {
		t.Error("CompactionRunner.RunCompaction was NOT called")
	}
}

// TestAutoCompaction_E2E_BelowThreshold verifies that compaction does NOT trigger
// when usage is below the threshold.
func TestAutoCompaction_E2E_BelowThreshold(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := InMemorySessionManager(cwd)
	settingsManager := NewSettingsManager(cwd, agentDir)

	model := &ai.Model{
		ID:            "test-model",
		Provider:      "test-provider",
		Api:           "openai-completions",
		ContextWindow: 100000,
		MaxTokens:     4096,
	}

	fakeStreamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent("Short response")},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage: ai.Usage{
					Input:       1000, // well below threshold
					Output:      200,
					TotalTokens: 1200,
				},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{
				Type:    ai.EventDone,
				Reason:  ai.StopReasonStop,
				Message: output,
			})
			stream.End(nil)
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return fakeStreamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	runner := &mockCompactionRunner{
		shouldCompactResult: false, // mock says don't compact
	}

	rl := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	session := NewAgentSession(AgentSessionOptions{
		Agent:            a,
		SessionManager:   sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), ""),
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer session.Close()

	var events []AgentSessionEvent
	var mu sync.Mutex
	session.Subscribe(func(e AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	err := session.Prompt("Hello")
	if err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}

	a.WaitForIdle()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	for _, e := range events {
		if e.Type == "auto_compaction_start" {
			t.Error("auto_compaction_start should NOT have been emitted for low usage")
		}
	}
	if runner.runCalled {
		t.Error("CompactionRunner.RunCompaction should NOT have been called")
	}
}

// TestAutoCompaction_E2E_GetContextUsage verifies that GetContextUsage returns
// the correct percentage based on the last assistant message's usage,
// NOT the accumulated totals across all messages.
func TestAutoCompaction_E2E_GetContextUsage(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := InMemorySessionManager(cwd)
	settingsManager := NewSettingsManager(cwd, agentDir)

	model := &ai.Model{
		ID:            "test-model",
		Provider:      "test-provider",
		Api:           "openai-completions",
		ContextWindow: 200000,
		MaxTokens:     4096,
	}

	turn := 0
	fakeStreamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			turn++
			// Each turn has increasing input (cumulative context) but modest numbers.
			// Turn 1: input=5000 (user msg + system prompt)
			// Turn 2: input=10000 (turn1 output is now in context)
			// Turn 3: input=15000
			inputTokens := turn * 5000
			outputTokens := 2000

			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent("Response for turn")},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage: ai.Usage{
					Input:       inputTokens,
					Output:      outputTokens,
					TotalTokens: inputTokens + outputTokens,
				},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{
				Type:    ai.EventDone,
				Reason:  ai.StopReasonStop,
				Message: output,
			})
			stream.End(nil)
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return fakeStreamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return ConvertToLLM(msgs)
		},
	})

	runner := &mockCompactionRunner{shouldCompactResult: false}

	rl := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	session := NewAgentSession(AgentSessionOptions{
		Agent:            a,
		SessionManager:   sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    NewModelRegistry(NewAuthStorage(filepath.Join(agentDir, "auth.json")), ""),
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer session.Close()

	// Run 3 turns
	for i := 0; i < 3; i++ {
		err := session.Prompt("Message " + string(rune('1'+i)))
		if err != nil {
			t.Fatalf("Prompt %d failed: %v", i+1, err)
		}
		a.WaitForIdle()
		time.Sleep(50 * time.Millisecond)
	}

	// Check accumulated stats (old approach - sums ALL messages)
	stats := session.GetSessionStats()
	accumulatedTotal := stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
	t.Logf("Accumulated stats: input=%d output=%d total=%d", stats.Tokens.Input, stats.Tokens.Output, accumulatedTotal)

	// Check context usage (new approach - uses last message only)
	cu := session.GetContextUsage()
	if cu == nil {
		t.Fatal("GetContextUsage returned nil")
	}
	t.Logf("Context usage: tokens=%d percent=%.1f%% window=%d", cu.Tokens, cu.Percent, cu.ContextWindow)

	// The accumulated total should be much larger than the actual context
	// Turn 1: input=5000, output=2000 → sum so far: 7000
	// Turn 2: input=10000, output=2000 → sum so far: 19000
	// Turn 3: input=15000, output=2000 → sum so far: 36000
	expectedAccumulated := (5000 + 10000 + 15000) + (2000 * 3) // 36000
	if accumulatedTotal != expectedAccumulated {
		t.Errorf("expected accumulated total %d, got %d", expectedAccumulated, accumulatedTotal)
	}

	// The actual context usage should be based on the LAST message: input=15000 + output=2000 = 17000
	expectedContext := 15000 + 2000 // 17000 from last message's totalTokens
	if cu.Tokens != expectedContext {
		t.Errorf("expected context tokens %d, got %d (accumulated was %d — should NOT use that!)",
			expectedContext, cu.Tokens, accumulatedTotal)
	}

	// Verify the percentage is reasonable
	expectedPercent := float64(expectedContext) / float64(200000) * 100 // 8.5%
	if cu.Percent < expectedPercent-0.1 || cu.Percent > expectedPercent+0.1 {
		t.Errorf("expected context percent ~%.1f%%, got %.1f%%", expectedPercent, cu.Percent)
	}

	// The accumulated approach would show 18% (36000/200000) — more than double
	accumulatedPercent := float64(accumulatedTotal) / float64(200000) * 100
	t.Logf("Accumulated would show %.1f%%, actual context is %.1f%% — difference: %.1f%%",
		accumulatedPercent, cu.Percent, accumulatedPercent-cu.Percent)

	if accumulatedPercent <= cu.Percent {
		t.Error("accumulated total should always be >= actual context usage")
	}
}
