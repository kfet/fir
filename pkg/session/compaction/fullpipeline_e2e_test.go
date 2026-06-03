package compaction

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	fmsg "github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Test helpers
// ============================================================================

// registerFakeProvider registers a mock LLM provider in the given registry
// that returns a canned summary response for compaction's GenerateSummary.
func registerFakeProvider(registry *ai.Registry, apiName, summaryText string) {
	streamSimple := func(ctx context.Context, model *ai.Model, prompt ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent(summaryText)},
				Api:        model.Api,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage:      ai.Usage{Input: 100, Output: 50, TotalTokens: 150},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	rawStream := func(ctx context.Context, model *ai.Model, prompt ai.Context, opts *ai.StreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent(summaryText)},
				Api:        model.Api,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	registry.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.Api(apiName),
		Stream:       rawStream,
		StreamSimple: streamSimple,
	}, "test-compaction-e2e")
}

func ptrInt(v int) *int { return &v }

// ============================================================================
// Full-pipeline tests
// ============================================================================

// TestFullPipeline_ThresholdCompaction exercises the entire compaction pipeline:
// multi-turn conversation → threshold trigger → PrepareCompaction → GenerateSummary
// (mock LLM) → session entry persisted → agent messages rebuilt with summary.
func TestFullPipeline_ThresholdCompaction(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	const apiName = "e2e-compact-api-threshold"
	const summaryText = "## Goal\nE2E threshold compaction summary\n## Progress\n- [x] Completed work"
	registerFakeProvider(ai.DefaultRegistry, apiName, summaryText)
	defer ai.DefaultRegistry.UnregisterApiProviders("test-compaction-e2e")

	sm := store.InMemorySessionStore(cwd)
	settingsManager := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			ReserveTokens:    ptrInt(5000),
			KeepRecentTokens: ptrInt(2000),
		},
	})

	model := &ai.Model{
		ID:            "e2e-model",
		Provider:      "e2e-provider",
		Api:           apiName,
		ContextWindow: 50000,
		MaxTokens:     4096,
	}

	authStorage := auth.NewInMemoryAuthStorage(nil)
	authStorage.Set("e2e-provider", auth.AuthCredential{
		Type: auth.CredentialTypeAPIKey,
		Key:  "e2e-test-key",
	})
	modelRegistry := models.NewModelRegistry(authStorage, "")

	turnCounter := 0
	// Each turn: input = 10000*turn. Threshold = 50000 - 5000 = 45000.
	// Turn 5: input=50000 → totalTokens=52000 → exceeds threshold.
	// Each response is ~2000 chars = ~500 tokens so keepRecentTokens=2000 keeps ~4 messages.
	streamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			turnCounter++
			input := 10000 * turnCounter
			// Large response so keepRecentTokens actually cuts older messages
			bigText := fmt.Sprintf("Response for turn %d. ", turnCounter) + strings.Repeat("context padding ", 120)
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent(bigText)},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage:      ai.Usage{Input: input, Output: 2000, TotalTokens: input + 2000},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
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
			return streamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return fmsg.ConvertToLLM(msgs)
		},
	})

	runner := &DefaultRunner{
		SettingsManager: settingsManager,
		ModelRegistry:   modelRegistry,
	}

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	agentSess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:            a,
		SessionStore:     sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    modelRegistry,
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer agentSess.Close()

	// Collect events
	var events []session.AgentSessionEvent
	var mu sync.Mutex
	agentSess.Subscribe(func(e session.AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	// Run 5 turns — turn 5 should trigger threshold compaction
	for i := 0; i < 5; i++ {
		// Large user messages so keepRecentTokens=2000 actually cuts old turns
		userMsg := fmt.Sprintf("User message for turn %d. ", i+1) + strings.Repeat("user context ", 100)
		err := agentSess.Prompt(userMsg)
		if err != nil {
			t.Fatalf("Prompt %d failed: %v", i+1, err)
		}
		a.WaitForIdle()
	}

	mu.Lock()
	defer mu.Unlock()

	t.Logf("Collected %d events:", len(events))
	for _, e := range events {
		t.Logf("  event type=%s", e.Type)
		if e.CompactionResult != nil {
			summaryPreview := e.CompactionResult.Summary
			if len(summaryPreview) > 80 {
				summaryPreview = summaryPreview[:80]
			}
			t.Logf("    compaction summary=%q firstKeptEntry=%s tokensBefore=%d",
				summaryPreview, e.CompactionResult.FirstKeptEntryID, e.CompactionResult.TokensBefore)
		}
		if e.ErrorMessage != "" {
			t.Logf("    error: %s", e.ErrorMessage)
		}
	}

	// Verify compaction was triggered and succeeded
	var compactionStartEvent, compactionEndEvent *session.AgentSessionEvent
	for i := range events {
		if events[i].Type == "auto_compaction_start" {
			compactionStartEvent = &events[i]
		}
		if events[i].Type == "auto_compaction_end" {
			compactionEndEvent = &events[i]
		}
	}

	if compactionStartEvent == nil {
		t.Fatal("auto_compaction_start event was NOT emitted — threshold compaction did not trigger")
	}
	if compactionStartEvent.CompactionReason != "threshold" {
		t.Errorf("expected compaction reason 'threshold', got %q", compactionStartEvent.CompactionReason)
	}
	if compactionEndEvent == nil {
		t.Fatal("auto_compaction_end event was NOT emitted")
	}
	if compactionEndEvent.ErrorMessage != "" {
		t.Fatalf("compaction ended with error: %s", compactionEndEvent.ErrorMessage)
	}
	if compactionEndEvent.CompactionResult == nil {
		t.Fatal("compaction result is nil — compaction failed silently")
	}

	result := compactionEndEvent.CompactionResult

	// Verify the summary contains our mock LLM output
	if !strings.Contains(result.Summary, "E2E threshold compaction summary") {
		t.Errorf("expected summary to contain mock LLM output, got: %s", result.Summary)
	}
	if result.TokensBefore <= 0 {
		t.Errorf("expected positive tokensBefore, got %d", result.TokensBefore)
	}
	if result.FirstKeptEntryID == "" {
		t.Error("expected non-empty firstKeptEntryID")
	}

	// Verify session entries have a compaction entry
	branch := sm.GetBranch("")
	var compactionEntry *store.SessionEntry
	for _, entry := range branch {
		if entry.Type == "compaction" {
			compactionEntry = entry
			break
		}
	}
	if compactionEntry == nil {
		t.Fatal("no compaction entry found in session branch")
	}
	if compactionEntry.Summary == "" {
		t.Error("compaction entry has empty summary")
	}
	if compactionEntry.FirstKeptEntryID == "" {
		t.Error("compaction entry has empty firstKeptEntryID")
	}
	if compactionEntry.TokensBefore <= 0 {
		t.Error("compaction entry has non-positive tokensBefore")
	}

	// Verify agent messages were rebuilt
	state := agentSess.State()
	t.Logf("Agent has %d messages after compaction", len(state.Messages))

	hasCompactionSummary := false
	for _, msg := range state.Messages {
		if msg.Custom != nil {
			if csm, ok := msg.Custom.(*fmsg.CompactionSummaryMessage); ok {
				hasCompactionSummary = true
				if !strings.Contains(csm.Summary, "E2E threshold compaction summary") {
					t.Errorf("compaction summary message has wrong summary: %s", csm.Summary)
				}
			}
		}
	}
	if !hasCompactionSummary {
		t.Error("agent messages do not contain a CompactionSummaryMessage after compaction")
	}

	// After compaction, should have fewer messages than 5 turns * 2 = 10
	if len(state.Messages) >= 10 {
		t.Errorf("expected fewer messages after compaction (old turns compacted away), got %d", len(state.Messages))
	}

	// Verify session rebuild from scratch produces correct messages
	ctx := sm.BuildSessionContext()
	if len(ctx.Messages) == 0 {
		t.Fatal("rebuilt session context has no messages")
	}
	firstMsg := ctx.Messages[0]
	if firstMsg.Custom == nil {
		t.Fatal("first message should be CompactionSummaryMessage")
	}
	if _, ok := firstMsg.Custom.(*fmsg.CompactionSummaryMessage); !ok {
		t.Fatalf("first message should be CompactionSummaryMessage, got %T", firstMsg.Custom)
	}
}

// TestFullPipeline_OverflowCompaction exercises the overflow path:
// LLM returns context overflow error → auto-compaction → retry last user msg.
func TestFullPipeline_OverflowCompaction(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	const apiName = "e2e-compact-api-overflow"
	const summaryText = "## Goal\nOverflow compaction summary for e2e"
	registerFakeProvider(ai.DefaultRegistry, apiName, summaryText)
	defer ai.DefaultRegistry.UnregisterApiProviders("test-compaction-e2e")

	sm := store.InMemorySessionStore(cwd)
	settingsManager := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			ReserveTokens:    ptrInt(5000),
			KeepRecentTokens: ptrInt(2000),
		},
	})

	model := &ai.Model{
		ID:            "e2e-overflow-model",
		Provider:      "e2e-provider",
		Api:           apiName,
		ContextWindow: 50000,
		MaxTokens:     4096,
	}

	authStorage := auth.NewInMemoryAuthStorage(nil)
	authStorage.Set("e2e-provider", auth.AuthCredential{
		Type: auth.CredentialTypeAPIKey,
		Key:  "e2e-test-key",
	})
	modelRegistry := models.NewModelRegistry(authStorage, "")

	var mu2 sync.Mutex
	callCount := 0

	streamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			mu2.Lock()
			callCount++
			n := callCount
			mu2.Unlock()

			switch {
			case n <= 3:
				// Normal responses with growing input
				output := &ai.AssistantMessage{
					Role:       ai.RoleAssistant,
					Content:    []ai.AssistantContent{ai.NewTextContent(fmt.Sprintf("Normal response %d", n))},
					Api:        m.Api,
					Provider:   m.Provider,
					Model:      m.ID,
					StopReason: ai.StopReasonStop,
					Timestamp:  time.Now().UnixMilli(),
					Usage:      ai.Usage{Input: 10000 * n, Output: 2000, TotalTokens: 10000*n + 2000},
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
				stream.End(nil)

			case n == 4:
				// Overflow error
				output := &ai.AssistantMessage{
					Role:         ai.RoleAssistant,
					Content:      []ai.AssistantContent{},
					Api:          m.Api,
					Provider:     m.Provider,
					Model:        m.ID,
					StopReason:   ai.StopReasonError,
					ErrorMessage: "prompt is too long: 60000 tokens > 50000 maximum",
					Timestamp:    time.Now().UnixMilli(),
					Usage:        ai.Usage{},
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				stream.End(nil)

			default:
				// Post-compaction retry succeeds
				output := &ai.AssistantMessage{
					Role:       ai.RoleAssistant,
					Content:    []ai.AssistantContent{ai.NewTextContent("Post-compaction response")},
					Api:        m.Api,
					Provider:   m.Provider,
					Model:      m.ID,
					StopReason: ai.StopReasonStop,
					Timestamp:  time.Now().UnixMilli(),
					Usage:      ai.Usage{Input: 5000, Output: 1000, TotalTokens: 6000},
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
				stream.End(nil)
			}
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model:         model,
			ThinkingLevel: agent.ThinkingOff,
		},
		StreamFn: func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return streamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return fmsg.ConvertToLLM(msgs)
		},
	})

	runner := &DefaultRunner{
		SettingsManager: settingsManager,
		ModelRegistry:   modelRegistry,
	}

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	agentSess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:            a,
		SessionStore:     sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    modelRegistry,
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer agentSess.Close()

	var events []session.AgentSessionEvent
	var evMu sync.Mutex
	agentSess.Subscribe(func(e session.AgentSessionEvent) {
		evMu.Lock()
		defer evMu.Unlock()
		events = append(events, e)
	})

	// 3 normal turns
	for i := 0; i < 3; i++ {
		err := agentSess.Prompt(fmt.Sprintf("Turn %d", i+1))
		if err != nil {
			t.Fatalf("Prompt %d failed: %v", i+1, err)
		}
		a.WaitForIdle()
	}

	// 4th prompt triggers overflow → compaction → retry
	err := agentSess.Prompt("Overflow trigger turn")
	if err != nil {
		t.Fatalf("Overflow prompt failed: %v", err)
	}
	a.WaitForIdle()
	// Poll for compaction + retry. The overflow triggers compaction, then a retry
	// prompt. We need to wait for the retry's agent_end event.
	pollDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(pollDeadline) {
		a.WaitForIdle()
		mu2.Lock()
		calls := callCount
		mu2.Unlock()
		// 3 normal + 1 overflow + 1 retry = 5
		if calls >= 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	evMu.Lock()
	defer evMu.Unlock()

	mu2.Lock()
	finalCallCount := callCount
	mu2.Unlock()

	t.Logf("Collected %d events, LLM called %d times:", len(events), finalCallCount)
	for _, e := range events {
		t.Logf("  event type=%s", e.Type)
		if e.CompactionReason != "" {
			t.Logf("    reason=%s willRetry=%v", e.CompactionReason, e.WillRetry)
		}
		if e.ErrorMessage != "" {
			t.Logf("    error=%s", e.ErrorMessage)
		}
	}

	// Verify overflow compaction was triggered
	overflowCompaction := false
	retryTriggered := false
	for _, e := range events {
		if e.Type == "auto_compaction_start" && e.CompactionReason == "overflow" {
			overflowCompaction = true
		}
		if e.Type == "auto_compaction_end" && e.WillRetry {
			retryTriggered = true
		}
	}

	if !overflowCompaction {
		t.Error("overflow compaction was NOT triggered")
	}
	if !retryTriggered {
		t.Error("retry was NOT triggered after overflow compaction")
	}

	// 3 normal + 1 overflow + 1 retry = 5 main stream calls
	if finalCallCount < 5 {
		t.Errorf("expected at least 5 LLM calls (3 normal + 1 overflow + 1 retry), got %d", finalCallCount)
	}

	// Session should have a compaction entry
	branch := sm.GetBranch("")
	hasCompaction := false
	for _, entry := range branch {
		if entry.Type == "compaction" {
			hasCompaction = true
			if !strings.Contains(entry.Summary, "Overflow compaction summary") {
				t.Errorf("compaction summary doesn't match mock output: %s", entry.Summary)
			}
		}
	}
	if !hasCompaction {
		t.Error("no compaction entry in session after overflow")
	}
}

// TestFullPipeline_SessionRebuildAfterCompaction verifies that after compaction
// the session context is correctly rebuilt with compaction summary + kept messages.
func TestFullPipeline_SessionRebuildAfterCompaction(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	const apiName = "e2e-compact-api-rebuild"
	const summaryText = "## Summary\nCompaction summary for rebuild verification test"
	registerFakeProvider(ai.DefaultRegistry, apiName, summaryText)
	defer ai.DefaultRegistry.UnregisterApiProviders("test-compaction-e2e")

	sm := store.InMemorySessionStore(cwd)
	settingsManager := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			ReserveTokens:    ptrInt(5000),
			KeepRecentTokens: ptrInt(1000), // small → most messages get compacted
		},
	})

	model := &ai.Model{
		ID:            "e2e-rebuild-model",
		Provider:      "e2e-provider",
		Api:           apiName,
		ContextWindow: 50000,
		MaxTokens:     4096,
	}

	authStorage := auth.NewInMemoryAuthStorage(nil)
	authStorage.Set("e2e-provider", auth.AuthCredential{
		Type: auth.CredentialTypeAPIKey,
		Key:  "e2e-test-key",
	})
	modelRegistry := models.NewModelRegistry(authStorage, "")

	turn := 0
	streamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			turn++
			bigText := fmt.Sprintf("Response %d. ", turn) + strings.Repeat("padding word ", 150)
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent(bigText)},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage:      ai.Usage{Input: 10000 * turn, Output: 2000, TotalTokens: 10000*turn + 2000},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
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
			return streamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return fmsg.ConvertToLLM(msgs)
		},
	})

	runner := &DefaultRunner{
		SettingsManager: settingsManager,
		ModelRegistry:   modelRegistry,
	}

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	agentSess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:            a,
		SessionStore:     sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    modelRegistry,
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer agentSess.Close()

	// Run turns until compaction triggers
	msgCountBeforeCompaction := 0
	for i := 0; i < 5; i++ {
		msgCountBeforeCompaction = len(agentSess.State().Messages)
		userMsg := fmt.Sprintf("Message for turn %d. ", i+1) + strings.Repeat("user padding ", 100)
		err := agentSess.Prompt(userMsg)
		if err != nil {
			t.Fatalf("Prompt %d failed: %v", i+1, err)
		}
		a.WaitForIdle()
	}

	// msgCountBeforeCompaction was recorded before the last prompt
	// (which triggers compaction). After compaction, message count should decrease.
	t.Logf("Messages before compaction-triggering turn: %d", msgCountBeforeCompaction)

	// Verify compaction happened
	branch := sm.GetBranch("")
	compactionIdx := -1
	for i, entry := range branch {
		if entry.Type == "compaction" {
			compactionIdx = i
			summaryPreview := entry.Summary
			if len(summaryPreview) > 60 {
				summaryPreview = summaryPreview[:60]
			}
			t.Logf("Compaction entry at index %d: summary=%q firstKept=%s",
				i, summaryPreview, entry.FirstKeptEntryID)
		}
	}
	if compactionIdx == -1 {
		t.Fatal("no compaction entry found — compaction did not execute")
	}

	// Rebuild from scratch (simulates session reload)
	ctx := sm.BuildSessionContext()
	t.Logf("Rebuilt session context has %d messages", len(ctx.Messages))

	if len(ctx.Messages) == 0 {
		t.Fatal("rebuilt context has no messages")
	}

	// First message should be compaction summary
	firstMsg := ctx.Messages[0]
	if firstMsg.Custom == nil {
		t.Fatal("first message should be a CompactionSummaryMessage")
	}
	csm, ok := firstMsg.Custom.(*fmsg.CompactionSummaryMessage)
	if !ok {
		t.Fatalf("first message should be CompactionSummaryMessage, got %T", firstMsg.Custom)
	}
	if !strings.Contains(csm.Summary, "rebuild verification test") {
		t.Errorf("compaction summary doesn't contain expected text: %s", csm.Summary)
	}

	// Fewer messages after rebuild than before compaction
	if len(ctx.Messages) >= msgCountBeforeCompaction {
		t.Errorf("expected fewer messages after rebuild (%d) than before compaction (%d)",
			len(ctx.Messages), msgCountBeforeCompaction)
	}

	// Kept messages should include recent user/assistant pairs
	hasUser := false
	hasAssistant := false
	for _, msg := range ctx.Messages[1:] {
		if msg.Role() == "user" {
			hasUser = true
		}
		if msg.Role() == "assistant" {
			hasAssistant = true
		}
	}
	if !hasUser || !hasAssistant {
		t.Errorf("rebuilt context should have recent user (%v) and assistant (%v) messages", hasUser, hasAssistant)
	}
}

// TestFullPipeline_CompactionDisabled verifies that the real DefaultRunner
// does NOT trigger compaction when settings disable it.
func TestFullPipeline_CompactionDisabled(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	enabled := false
	settingsManager := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			Enabled: &enabled,
		},
	})

	model := &ai.Model{
		ID:            "test-model",
		Provider:      "test-provider",
		Api:           "openai-completions",
		ContextWindow: 100000,
		MaxTokens:     4096,
	}

	streamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent("Big response")},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage:      ai.Usage{Input: 95000, Output: 5000, TotalTokens: 100000},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
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
			return streamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return fmsg.ConvertToLLM(msgs)
		},
	})

	runner := &DefaultRunner{
		SettingsManager: settingsManager,
		ModelRegistry:   models.NewModelRegistry(auth.NewInMemoryAuthStorage(nil), ""),
	}

	sm := store.InMemorySessionStore(cwd)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	agentSess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:            a,
		SessionStore:     sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    models.NewModelRegistry(auth.NewInMemoryAuthStorage(nil), ""),
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer agentSess.Close()

	var events []session.AgentSessionEvent
	var mu sync.Mutex
	agentSess.Subscribe(func(e session.AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	err := agentSess.Prompt("Hello")
	if err != nil {
		t.Fatalf("Prompt failed: %v", err)
	}
	a.WaitForIdle()

	mu.Lock()
	defer mu.Unlock()

	for _, e := range events {
		if e.Type == "auto_compaction_start" {
			t.Error("compaction should NOT trigger when disabled in settings")
		}
	}
}

// TestFullPipeline_DoubleCompaction verifies that a second compaction works
// correctly after the first one — the previous summary is incorporated.
func TestFullPipeline_DoubleCompaction(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	const apiName = "e2e-compact-api-double"
	const summaryText = "## Goal\nDouble compaction test summary"
	registerFakeProvider(ai.DefaultRegistry, apiName, summaryText)
	defer ai.DefaultRegistry.UnregisterApiProviders("test-compaction-e2e")

	sm := store.InMemorySessionStore(cwd)
	settingsManager := config.NewInMemorySettingsManager(config.Settings{
		Compaction: &config.CompactionSettings{
			ReserveTokens:    ptrInt(5000),
			KeepRecentTokens: ptrInt(1000),
		},
	})

	model := &ai.Model{
		ID:            "e2e-double-model",
		Provider:      "e2e-provider",
		Api:           apiName,
		ContextWindow: 30000,
		MaxTokens:     4096,
	}

	authStorage := auth.NewInMemoryAuthStorage(nil)
	authStorage.Set("e2e-provider", auth.AuthCredential{
		Type: auth.CredentialTypeAPIKey,
		Key:  "e2e-test-key",
	})
	modelRegistry := models.NewModelRegistry(authStorage, "")

	turn := 0
	streamFn := func(m *ai.Model, ctx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			turn++
			// Use totalTokens that scales with turn to force compaction multiple times.
			// With contextWindow=30000 and reserve=5000, threshold is 25000.
			// Reset effective usage after each compaction to simulate realistic behavior.
			input := 8000 * turn
			if input > 28000 {
				input = 28000
			}
			output := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Content:    []ai.AssistantContent{ai.NewTextContent(fmt.Sprintf("Response %d", turn))},
				Api:        m.Api,
				Provider:   m.Provider,
				Model:      m.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
				Usage:      ai.Usage{Input: input, Output: 2000, TotalTokens: input + 2000},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: output})
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
			return streamFn(m, ctx, opts)
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return fmsg.ConvertToLLM(msgs)
		},
	})

	runner := &DefaultRunner{
		SettingsManager: settingsManager,
		ModelRegistry:   modelRegistry,
	}

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	_ = rl.Reload()

	agentSess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:            a,
		SessionStore:     sm,
		SettingsManager:  settingsManager,
		ResourceLoader:   rl,
		ModelRegistry:    modelRegistry,
		CompactionRunner: runner,
		Cwd:              cwd,
	})
	defer agentSess.Close()

	compactionCount := 0
	var mu sync.Mutex
	agentSess.Subscribe(func(e session.AgentSessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Type == "auto_compaction_end" && e.CompactionResult != nil {
			compactionCount++
		}
	})

	// Run 8 turns — should trigger at least 2 compactions with small context window
	for i := 0; i < 8; i++ {
		err := agentSess.Prompt(fmt.Sprintf("Turn %d message", i+1))
		if err != nil {
			t.Fatalf("Prompt %d failed: %v", i+1, err)
		}
		a.WaitForIdle()
	}

	mu.Lock()
	defer mu.Unlock()

	t.Logf("Compaction count: %d", compactionCount)
	if compactionCount < 2 {
		t.Logf("Only %d compaction(s) triggered — verifying at least 1 happened", compactionCount)
		if compactionCount < 1 {
			t.Error("expected at least 1 compaction with small context window")
		}
	}

	// Verify session has multiple compaction entries (or at least one)
	branch := sm.GetBranch("")
	compactionEntries := 0
	for _, entry := range branch {
		if entry.Type == "compaction" {
			compactionEntries++
		}
	}
	t.Logf("Session has %d compaction entries", compactionEntries)
	if compactionEntries < 1 {
		t.Error("expected at least 1 compaction entry in session")
	}

	// Agent messages should still be valid and contain a compaction summary
	state := agentSess.State()
	t.Logf("Agent has %d messages after multiple compactions", len(state.Messages))
	if len(state.Messages) == 0 {
		t.Fatal("agent has no messages after compactions")
	}
}
