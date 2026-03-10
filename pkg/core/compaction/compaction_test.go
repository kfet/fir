package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/session"
	fmsg "github.com/kfet/fir/pkg/session"
)

func makeUserEntry(id, text string) *session.SessionEntry {
	msg := ai.NewUserMsg(text, 0)
	raw, _ := json.Marshal(msg)
	return &session.SessionEntry{
		Type:       "message",
		ID:         id,
		RawMessage: raw,
	}
}

func makeAssistantEntry(id, text string, usage *ai.Usage) *session.SessionEntry {
	content := []ai.AssistantContent{ai.NewTextContent(text)}
	am := ai.AssistantMessage{
		Content:    content,
		StopReason: ai.StopReasonStop,
	}
	if usage != nil {
		am.Usage = *usage
	}
	msg := ai.NewAssistantMsg(am)
	raw, _ := json.Marshal(msg)
	return &session.SessionEntry{
		Type:       "message",
		ID:         id,
		RawMessage: raw,
	}
}

func makeToolResultEntry(id string) *session.SessionEntry {
	msg := ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "read",
		Content:    []ai.ToolResultContent{{Type: "text", Text: "result"}},
	})
	raw, _ := json.Marshal(msg)
	return &session.SessionEntry{
		Type:       "message",
		ID:         id,
		RawMessage: raw,
	}
}

func TestEstimateTokens_User(t *testing.T) {
	text := strings.Repeat("x", 100)
	msg := ai.NewUserMsg(text, 0)
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	if tokens != 25 {
		t.Errorf("expected 25, got %d", tokens)
	}
}

func TestEstimateTokens_Assistant(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("Hello world")},
	})
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	if tokens != 3 {
		t.Errorf("expected 3, got %d", tokens)
	}
}

func TestCalculateContextTokens_Value(t *testing.T) {
	usage := ai.Usage{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}
	tokens := CalculateContextTokens(usage)
	if tokens != 165 {
		t.Errorf("expected 165, got %d", tokens)
	}
}

func TestCalculateContextTokens_WithTotal(t *testing.T) {
	usage := ai.Usage{TotalTokens: 200, Input: 100, Output: 50}
	tokens := CalculateContextTokens(usage)
	if tokens != 200 {
		t.Errorf("expected 200, got %d", tokens)
	}
}

func TestShouldCompact_Under(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 5000}
	if ShouldCompact(5000, 10000, settings, 0.0) {
		t.Error("should not compact when under threshold")
	}
}

func TestShouldCompact_Over(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 5000}
	if !ShouldCompact(9500, 10000, settings, 0.0) {
		t.Error("should compact when over threshold")
	}
}

func TestShouldCompact_Disabled(t *testing.T) {
	settings := CompactionSettings{Enabled: false, ReserveTokens: 1000, KeepRecentTokens: 5000}
	if ShouldCompact(9500, 10000, settings, 0.0) {
		t.Error("should not compact when disabled")
	}
}

// TestShouldCompact_FillRatioLower verifies that a fillRatio of 0.5 triggers
// compaction earlier than the default 0.90.
//
// ShouldCompact uses a two-part check on the fill-ratio path:
//  1. Gate: skip if ratio < fillRatio
//  2. Trigger: contextTokens > contextWindow - ReserveTokens
//
// To show the lower ratio fires sooner, we pick ReserveTokens large enough
// that the reserve threshold (contextWindow - ReserveTokens = 5500) sits in
// the 50–90% band. At 60% fill (6000 tokens) the gate passes for fillRatio=0.5
// and the reserve check fires, but the default gate (0.90) would have blocked it.
func TestShouldCompact_FillRatioLower(t *testing.T) {
	settings := CompactionSettings{
		Enabled:       true,
		ReserveTokens: 4500, // threshold = 10000 - 4500 = 5500
		FillRatio:     0.5,
	}
	// 60% fill (6000 > 5500) — lower ratio gate passes, reserve check fires.
	if !ShouldCompact(6000, 10000, settings, 0.0) {
		t.Error("fillRatio=0.5 should compact at 60% fill")
	}
	// Same token count with default fillRatio=0.0 (→ 0.90): gate blocks (0.6 < 0.9).
	defaultSettings := CompactionSettings{Enabled: true, ReserveTokens: 4500}
	if ShouldCompact(6000, 10000, defaultSettings, 0.0) {
		t.Error("default fillRatio should NOT compact at 60% fill")
	}
	// At 40% fill (4000 < 5500) the reserve check itself doesn't fire.
	if ShouldCompact(4000, 10000, settings, 0.0) {
		t.Error("fillRatio=0.5 should not compact at 40% fill")
	}
}

// TestShouldCompact_FillRatioDisabled verifies that a fillRatio > 1.0
// effectively disables ratio-based compaction.
func TestShouldCompact_FillRatioDisabled(t *testing.T) {
	settings := CompactionSettings{
		Enabled:       true,
		ReserveTokens: 100,
		FillRatio:     2.0, // impossible to reach
	}
	// Even at 99% fill the ratio path should not fire.
	if ShouldCompact(9900, 10000, settings, 0.0) {
		t.Error("fillRatio=2.0 should not trigger ratio-based compaction")
	}
}

// TestShouldCompact_MaxContextTokens verifies that MaxContextTokens triggers
// compaction when exceeded, even when the fill ratio is low.
func TestShouldCompact_MaxContextTokens(t *testing.T) {
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    1000,
		MaxContextTokens: 5000,
	}
	// 6000 tokens > cap of 5000 — must compact.
	if !ShouldCompact(6000, 100000, settings, 0.0) {
		t.Error("should compact when MaxContextTokens exceeded (low fill ratio)")
	}
	// 4000 tokens < cap — should not compact.
	if ShouldCompact(4000, 100000, settings, 0.0) {
		t.Error("should not compact when under MaxContextTokens")
	}
}

// TestShouldCompact_MaxContextCost verifies that MaxContextCost triggers
// compaction when the estimated input cost exceeds the threshold.
func TestShouldCompact_MaxContextCost(t *testing.T) {
	// 100 000 tokens * $15/MTok = $1.50/turn → above $1.00 threshold.
	settings := CompactionSettings{
		Enabled:        true,
		ReserveTokens:  1000,
		MaxContextCost: 1.00,
	}
	if !ShouldCompact(100_000, 200_000, settings, 15.0) {
		t.Error("should compact when cost exceeds MaxContextCost")
	}
	// 50 000 tokens * $15/MTok = $0.75/turn → below threshold.
	if ShouldCompact(50_000, 200_000, settings, 15.0) {
		t.Error("should not compact when cost is below MaxContextCost")
	}
}

// TestShouldCompact_MaxContextCostZeroPricing verifies that MaxContextCost
// does NOT trigger when inputCostPerMTok is 0 (unknown pricing).
func TestShouldCompact_MaxContextCostZeroPricing(t *testing.T) {
	settings := CompactionSettings{
		Enabled:        true,
		ReserveTokens:  1000,
		MaxContextCost: 0.01, // very low threshold
	}
	// Even with 1M tokens, unknown pricing means cost path must be skipped.
	if ShouldCompact(1_000_000, 2_000_000, settings, 0.0) {
		t.Error("cost-based compaction should not fire when inputCostPerMTok=0")
	}
}

// TestShouldCompact_MaxContextCostVerySmall verifies that a very small
// MaxContextCost threshold triggers at low token counts.
func TestShouldCompact_MaxContextCostVerySmall(t *testing.T) {
	// 1000 tokens * $15/MTok = $0.015/turn → above $0.001 threshold.
	settings := CompactionSettings{
		Enabled:        true,
		ReserveTokens:  100,
		MaxContextCost: 0.001,
	}
	if !ShouldCompact(1000, 200_000, settings, 15.0) {
		t.Error("very small MaxContextCost should trigger at low token counts")
	}
}

// TestShouldCompact_NegativeFieldsTreatedAsZero verifies that negative values
// for FillRatio, MaxContextTokens, and MaxContextCost are treated as
// zero/disabled (i.e. the default fill-ratio path is used, caps are skipped).
func TestShouldCompact_NegativeFieldsTreatedAsZero(t *testing.T) {
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    1000,
		FillRatio:        -0.5, // treated as 0 → default 0.90
		MaxContextTokens: -100, // treated as disabled
		MaxContextCost:   -1.0, // treated as disabled
	}
	// At 95% fill the default ratio path should still fire.
	if !ShouldCompact(9500, 10000, settings, 5.0) {
		t.Error("negative FillRatio should fall back to default 0.90 and trigger at 95%")
	}
	// At 50% fill should not trigger (no caps active, ratio not exceeded).
	if ShouldCompact(5000, 10000, settings, 5.0) {
		t.Error("negative fields should not cause false positives at 50% fill")
	}
}

// TestShouldCompact_ZeroFieldsPreserveDefaults verifies that zero-valued new
// fields leave the existing default behavior completely unchanged.
func TestShouldCompact_ZeroFieldsPreserveDefaults(t *testing.T) {
	base := CompactionSettings{Enabled: true, ReserveTokens: 1000}
	// FillRatio==0 → default 0.90; MaxContextTokens==0 and MaxContextCost==0 → disabled.

	// Under default 0.90 threshold → no compact.
	if ShouldCompact(5000, 10000, base, 0.0) {
		t.Error("zero-valued new fields: should not compact at 50% fill")
	}
	// Over 0.90 threshold with insufficient headroom → compact.
	if !ShouldCompact(9500, 10000, base, 0.0) {
		t.Error("zero-valued new fields: should compact at 95% fill")
	}
}

func TestEstimateContextTokens_NoUsage(t *testing.T) {
	messages := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello world", 0)),
	}
	result := EstimateContextTokens(messages)
	if result.Tokens != 3 {
		t.Errorf("expected 3, got %d", result.Tokens)
	}
}

func TestFindCutPoint_Basic(t *testing.T) {
	entries := []*session.SessionEntry{
		makeUserEntry("1", strings.Repeat("x", 100)),
		makeAssistantEntry("2", strings.Repeat("x", 100), nil),
		makeUserEntry("3", strings.Repeat("x", 100)),
		makeAssistantEntry("4", strings.Repeat("x", 100), nil),
	}
	result := FindCutPoint(entries, 0, len(entries), 30)
	if result.FirstKeptEntryIndex < 1 {
		t.Errorf("expected cut, firstKept=%d", result.FirstKeptEntryIndex)
	}
}

func TestFindCutPoint_Empty(t *testing.T) {
	result := FindCutPoint(nil, 0, 0, 1000)
	if result.FirstKeptEntryIndex != 0 {
		t.Errorf("expected 0, got %d", result.FirstKeptEntryIndex)
	}
}

func TestFindTurnStartIndex_FromToolResult(t *testing.T) {
	entries := []*session.SessionEntry{
		makeUserEntry("1", "q"),
		makeAssistantEntry("2", "r", nil),
		makeToolResultEntry("3"),
	}
	idx := FindTurnStartIndex(entries, 2, 0)
	if idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}

func TestDefaultCompactionSettings_Values(t *testing.T) {
	s := DefaultCompactionSettings
	if !s.Enabled {
		t.Error("expected enabled")
	}
	if s.ReserveTokens != 16384 {
		t.Errorf("expected 16384, got %d", s.ReserveTokens)
	}
}

func TestEstimateTokens_ToolResult(t *testing.T) {
	msg := ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "read",
		Content: []ai.ToolResultContent{
			{Type: "text", Text: strings.Repeat("a", 200)},
		},
	})
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	if tokens != 50 {
		t.Errorf("expected 50, got %d", tokens)
	}
}

func TestEstimateTokens_ToolCallInAssistant(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("tc1", "read", map[string]any{"path": "test.txt"}),
		},
	})
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	if tokens < 1 {
		t.Error("expected at least 1 token for tool call")
	}
}

func TestEstimateTokens_ThinkingBlock(t *testing.T) {
	msg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewThinkingContent(strings.Repeat("t", 400)),
			ai.NewTextContent("result"),
		},
	})
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	// 400 thinking chars + 6 text chars = 406, / 4 = 101.5, ceil = 102
	if tokens != 102 {
		t.Errorf("expected 102, got %d", tokens)
	}
}

func TestEstimateTokens_MixedContent(t *testing.T) {
	// User message
	user := agent.NewAgentMessage(ai.NewUserMsg(strings.Repeat("u", 80), 0))
	uTokens := EstimateTokens(user)

	// Assistant with text + tool call
	asst := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewTextContent(strings.Repeat("a", 40)),
			ai.NewToolCallContent("tc1", "bash", map[string]any{"command": "ls"}),
		},
	}))
	aTokens := EstimateTokens(asst)

	// Tool result
	tr := agent.NewAgentMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "bash",
		Content:    []ai.ToolResultContent{{Type: "text", Text: strings.Repeat("r", 120)}},
	}))
	tTokens := EstimateTokens(tr)

	if uTokens != 20 {
		t.Errorf("user tokens: expected 20, got %d", uTokens)
	}
	if aTokens < 10 {
		t.Errorf("assistant tokens: expected at least 10, got %d", aTokens)
	}
	if tTokens != 30 {
		t.Errorf("tool result tokens: expected 30, got %d", tTokens)
	}
}

func TestEstimateContextTokens_WithUsage(t *testing.T) {
	messages := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello", 0)),
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Content:    []ai.AssistantContent{ai.NewTextContent("response")},
			StopReason: ai.StopReasonStop,
			Usage:      ai.Usage{Input: 100, Output: 50, CacheRead: 10},
		})),
		agent.NewAgentMessage(ai.NewUserMsg("follow up question here", 0)),
	}
	result := EstimateContextTokens(messages)
	if result.UsageTokens != 160 {
		t.Errorf("usage tokens: expected 160, got %d", result.UsageTokens)
	}
	if result.LastUsageIndex == nil {
		t.Fatal("expected last usage index")
	}
	if *result.LastUsageIndex != 1 {
		t.Errorf("expected last usage index 1, got %d", *result.LastUsageIndex)
	}
	if result.TrailingTokens < 1 {
		t.Error("expected trailing tokens > 0")
	}
	if result.Tokens != result.UsageTokens+result.TrailingTokens {
		t.Errorf("total (%d) should equal usage (%d) + trailing (%d)", result.Tokens, result.UsageTokens, result.TrailingTokens)
	}
}

func TestEstimateContextTokens_ErrorMessageSkipped(t *testing.T) {
	messages := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello", 0)),
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Content:    []ai.AssistantContent{},
			StopReason: ai.StopReasonError,
			Usage:      ai.Usage{Input: 999, Output: 999},
		})),
	}
	result := EstimateContextTokens(messages)
	// Error messages should be skipped for usage, so we fall through to estimation
	if result.LastUsageIndex != nil {
		t.Error("error message usage should not be used")
	}
}

func TestPrepareCompaction_MinimalSession(t *testing.T) {
	// Only 2 entries — should still produce a preparation
	entries := []*session.SessionEntry{
		makeUserEntry("1", strings.Repeat("x", 400)),
		makeAssistantEntry("2", strings.Repeat("y", 400), nil),
	}
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    100,
		KeepRecentTokens: 10, // very low to force cut early
	}
	prep := PrepareCompaction(entries, settings)
	if prep == nil {
		t.Fatal("expected preparation for 2-entry session")
	}
	if prep.FirstKeptEntryID == "" {
		t.Error("expected first kept entry ID")
	}
}

func TestPrepareCompaction_EndsWithCompaction(t *testing.T) {
	entries := []*session.SessionEntry{
		makeUserEntry("1", "hello"),
		makeAssistantEntry("2", "world", nil),
		{Type: "compaction", ID: "3", Summary: "summary"},
	}
	settings := DefaultCompactionSettings
	prep := PrepareCompaction(entries, settings)
	if prep != nil {
		t.Error("should return nil when last entry is compaction")
	}
}

func TestPrepareCompaction_WithPreviousCompaction(t *testing.T) {
	entries := []*session.SessionEntry{
		makeUserEntry("1", strings.Repeat("a", 200)),
		makeAssistantEntry("2", strings.Repeat("b", 200), nil),
		{Type: "compaction", ID: "3", Summary: "previous summary"},
		makeUserEntry("4", strings.Repeat("c", 200)),
		makeAssistantEntry("5", strings.Repeat("d", 200), nil),
		makeUserEntry("6", strings.Repeat("e", 200)),
		makeAssistantEntry("7", strings.Repeat("f", 200), nil),
	}
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    100,
		KeepRecentTokens: 50, // low to force cut
	}
	prep := PrepareCompaction(entries, settings)
	if prep == nil {
		t.Fatal("expected preparation")
	}
	if prep.PreviousSummary != "previous summary" {
		t.Errorf("expected previous summary, got %q", prep.PreviousSummary)
	}
	// Messages to summarize should only come from after the compaction entry
	for _, msg := range prep.MessagesToSummarize {
		if msg.Role() == "" {
			t.Error("invalid message in summarize list")
		}
	}
}

func TestPrepareCompaction_SplitTurn(t *testing.T) {
	// Create a scenario where the cut point falls inside a turn (mid tool results)
	entries := []*session.SessionEntry{
		makeUserEntry("1", strings.Repeat("x", 400)),
		makeAssistantEntry("2", strings.Repeat("y", 400), nil),
		makeUserEntry("3", strings.Repeat("a", 400)),   // turn start
		makeAssistantEntry("4", strings.Repeat("b", 400), nil),
		makeToolResultEntry("5"),                        // mid turn
		makeAssistantEntry("6", strings.Repeat("c", 400), nil),
	}
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    100,
		KeepRecentTokens: 50, // very low to force cut into the turn
	}
	prep := PrepareCompaction(entries, settings)
	if prep == nil {
		t.Fatal("expected preparation")
	}
	// If cut point is at tool result or assistant within a turn, IsSplitTurn should be true
	// and TurnPrefixMessages should contain the messages from turn start to cut point
	if prep.IsSplitTurn {
		if len(prep.TurnPrefixMessages) == 0 {
			t.Error("split turn should have turn prefix messages")
		}
	}
}

func TestEstimateTokens_CustomBashExecution(t *testing.T) {
	msg := agent.AgentMessage{
		Custom: &fmsg.BashExecutionMessage{
			Command: strings.Repeat("c", 80),
			Output:  strings.Repeat("o", 120),
		},
	}
	// (80 + 120) / 4 = 50
	tokens := EstimateTokens(msg)
	if tokens != 50 {
		t.Errorf("expected 50, got %d", tokens)
	}
}

func TestEstimateTokens_CustomBranchSummary(t *testing.T) {
	msg := agent.AgentMessage{
		Custom: &fmsg.BranchSummaryMessage{
			Summary: strings.Repeat("s", 200),
		},
	}
	tokens := EstimateTokens(msg)
	if tokens != 50 {
		t.Errorf("expected 50, got %d", tokens)
	}
}

func TestEstimateTokens_CustomCompactionSummary(t *testing.T) {
	msg := agent.AgentMessage{
		Custom: &fmsg.CompactionSummaryMessage{
			Summary: strings.Repeat("s", 100),
		},
	}
	tokens := EstimateTokens(msg)
	if tokens != 25 {
		t.Errorf("expected 25, got %d", tokens)
	}
}

func TestEstimateTokens_CustomMessage(t *testing.T) {
	msg := agent.AgentMessage{
		Custom: &fmsg.CustomMessage{
			Content: strings.Repeat("x", 40),
		},
	}
	tokens := EstimateTokens(msg)
	if tokens != 10 {
		t.Errorf("expected 10, got %d", tokens)
	}
}

func TestEstimateTokens_CustomMessageNonString(t *testing.T) {
	msg := agent.AgentMessage{
		Custom: &fmsg.CustomMessage{
			Content: map[string]any{"key": "value"},
		},
	}
	tokens := EstimateTokens(msg)
	if tokens != 0 {
		t.Errorf("expected 0 for non-string custom content, got %d", tokens)
	}
}

func TestEstimateTokens_ImageInToolResult(t *testing.T) {
	msg := ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "tc1",
		ToolName:   "read",
		Content: []ai.ToolResultContent{
			{Type: "image", MimeType: "image/png", Data: "base64data"},
		},
	})
	tokens := EstimateTokens(agent.NewAgentMessage(msg))
	// Image should count as 4800 chars / 4 = 1200
	if tokens != 1200 {
		t.Errorf("expected 1200 for image, got %d", tokens)
	}
}

func TestEstimateTokens_EmptyMessages(t *testing.T) {
	// Empty user message
	emptyUser := agent.AgentMessage{Message: ai.Message{}}
	if tokens := EstimateTokens(emptyUser); tokens != 0 {
		t.Errorf("expected 0 for empty message, got %d", tokens)
	}
}

func TestExtractTextFromResponse(t *testing.T) {
	response := &ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewTextContent("first part"),
			ai.NewThinkingContent("some thinking"),
			ai.NewTextContent("second part"),
		},
	}
	result := extractTextFromResponse(response)
	if result != "first part\nsecond part" {
		t.Errorf("expected 'first part\\nsecond part', got %q", result)
	}
}

func TestExtractTextFromResponse_Empty(t *testing.T) {
	response := &ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewThinkingContent("only thinking"),
		},
	}
	result := extractTextFromResponse(response)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestGetMessageFromEntry_CustomMessage(t *testing.T) {
	content, _ := json.Marshal("test content")
	entry := &session.SessionEntry{
		Type:       "custom_message",
		ID:         "cm1",
		CustomType: "my-extension",
		Content:    content,
		Display:    true,
		Timestamp:  "2024-01-01T00:00:00Z",
	}
	msg := getMessageFromEntry(entry)
	if msg == nil {
		t.Fatal("expected message for custom_message entry")
	}
	if msg.Custom == nil {
		t.Error("expected custom message")
	}
}

func TestGetMessageFromEntry_BranchSummary(t *testing.T) {
	entry := &session.SessionEntry{
		Type:      "branch_summary",
		ID:        "bs1",
		Summary:   "Branch summary text",
		FromID:    "from-id",
		Timestamp: "2024-01-01T00:00:00Z",
	}
	msg := getMessageFromEntry(entry)
	if msg == nil {
		t.Fatal("expected message for branch_summary entry")
	}
}

func TestGetMessageFromEntry_CompactionEntry(t *testing.T) {
	entry := &session.SessionEntry{
		Type:      "compaction",
		ID:        "c1",
		Summary:   "Compaction summary",
		Timestamp: "2024-01-01T00:00:00Z",
	}
	msg := getMessageFromEntry(entry)
	if msg == nil {
		t.Fatal("expected message for compaction entry")
	}
}

func TestGetMessageFromEntry_EmptyMessage(t *testing.T) {
	entry := &session.SessionEntry{
		Type: "message",
		ID:   "m1",
	}
	msg := getMessageFromEntry(entry)
	if msg != nil {
		t.Error("expected nil for entry with empty RawMessage")
	}
}

func TestGetMessageFromEntry_InvalidJSON(t *testing.T) {
	entry := &session.SessionEntry{
		Type:       "message",
		ID:         "m1",
		RawMessage: json.RawMessage(`{invalid`),
	}
	msg := getMessageFromEntry(entry)
	if msg != nil {
		t.Error("expected nil for entry with invalid JSON")
	}
}

func TestExtractFileOperations_WithPrevCompaction(t *testing.T) {
	details, _ := json.Marshal(CompactionDetails{
		ReadFiles:     []string{"/prev/read.go"},
		ModifiedFiles: []string{"/prev/mod.go"},
	})
	entries := []*session.SessionEntry{
		{Type: "compaction", ID: "c1", Details: details},
	}

	assistantMsg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "read", map[string]any{"path": "/new/read.go"}),
		},
	})
	messages := []agent.AgentMessage{agent.NewAgentMessage(assistantMsg)}

	fileOps := extractFileOperations(messages, entries, 0)
	if _, ok := fileOps.Read["/prev/read.go"]; !ok {
		t.Error("expected /prev/read.go from previous compaction details")
	}
	if _, ok := fileOps.Edited["/prev/mod.go"]; !ok {
		t.Error("expected /prev/mod.go from previous compaction details")
	}
	if _, ok := fileOps.Read["/new/read.go"]; !ok {
		t.Error("expected /new/read.go from current messages")
	}
}

func TestPrepareCompaction_EmptyEntries(t *testing.T) {
	prep := PrepareCompaction(nil, DefaultCompactionSettings)
	if prep != nil {
		t.Error("expected nil for empty entries")
	}
}

func TestBuildSummarizationPrompt_WithCustomInstructions(t *testing.T) {
	prompt := BuildSummarizationPrompt("convo", "", "focus on Go code")
	if !strings.Contains(prompt, "focus on Go code") {
		t.Error("expected custom instructions in prompt")
	}
}

func TestBuildSummarizationPrompt_UpdateWithCustomInstructions(t *testing.T) {
	prompt := BuildSummarizationPrompt("convo", "old summary", "focus on tests")
	if !strings.Contains(prompt, "<previous-summary>") {
		t.Error("expected <previous-summary>")
	}
	if !strings.Contains(prompt, "focus on tests") {
		t.Error("expected custom instructions in prompt")
	}
}

func TestFindCutPoint_SingleEntry(t *testing.T) {
	entries := []*session.SessionEntry{
		makeUserEntry("1", "hello"),
	}
	result := FindCutPoint(entries, 0, 1, 1000)
	if result.FirstKeptEntryIndex != 0 {
		t.Errorf("expected 0, got %d", result.FirstKeptEntryIndex)
	}
}

func TestFindTurnStartIndex_NoneFound(t *testing.T) {
	entries := []*session.SessionEntry{
		makeAssistantEntry("1", "response", nil),
		makeToolResultEntry("2"),
	}
	idx := FindTurnStartIndex(entries, 1, 0)
	// No user message found, should return -1
	if idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}

func TestBuildSummarizationPrompt_Initial(t *testing.T) {
	prompt := BuildSummarizationPrompt("convo", "", "")
	if !strings.Contains(prompt, "<conversation>") {
		t.Error("expected <conversation>")
	}
	if strings.Contains(prompt, "<previous-summary>") {
		t.Error("should not contain previous-summary")
	}
}

func TestBuildSummarizationPrompt_Update(t *testing.T) {
	prompt := BuildSummarizationPrompt("convo", "old", "")
	if !strings.Contains(prompt, "<previous-summary>") {
		t.Error("expected <previous-summary>")
	}
}

// TestGenerateSummary_ProgressCallback verifies that the CompactionProgressFunc
// attached to the context is called with text deltas during streaming.
func TestGenerateSummary_ProgressCallback(t *testing.T) {
	const apiName = "test-generate-summary-progress"
	const part1 = "## Goal\n"
	const part2 = "summarize things"

	// Register a fake provider that emits two text-delta events.
	registry := ai.NewRegistry()
	registry.RegisterApiProvider(&ai.ApiProvider{
		Api: ai.Api(apiName),
		StreamSimple: func(ctx context.Context, model *ai.Model, prompt ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			stream := ai.NewAssistantMessageEventStream()
			go func() {
				// partial after first delta
				partial1 := &ai.AssistantMessage{
					Role:    ai.RoleAssistant,
					Content: []ai.AssistantContent{ai.NewTextContent(part1)},
					Api:     model.Api, Provider: model.Provider, Model: model.ID,
				}
				// partial after second delta (accumulated text)
				partial2 := &ai.AssistantMessage{
					Role:    ai.RoleAssistant,
					Content: []ai.AssistantContent{ai.NewTextContent(part1 + part2)},
					Api:     model.Api, Provider: model.Provider, Model: model.ID,
				}
				final := &ai.AssistantMessage{
					Role:       ai.RoleAssistant,
					Content:    []ai.AssistantContent{ai.NewTextContent(part1 + part2)},
					Api:        model.Api, Provider: model.Provider, Model: model.ID,
					StopReason: ai.StopReasonStop,
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: partial1})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, Partial: partial1})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, Partial: partial2})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopReasonStop, Message: final})
				stream.End(nil)
			}()
			return stream
		},
		Stream: func(ctx context.Context, model *ai.Model, prompt ai.Context, opts *ai.StreamOptions) *ai.AssistantMessageEventStream {
			s := ai.NewAssistantMessageEventStream()
			go func() { s.End(nil) }()
			return s
		},
	}, "test-progress-owner")
	defer registry.UnregisterApiProviders("test-progress-owner")

	model := &ai.Model{ID: "test", Provider: "test", Api: ai.Api(apiName)}

	var phases []string
	var deltas []string
	progressFn := core.CompactionProgressFunc(func(phase, delta string) {
		phases = append(phases, phase)
		deltas = append(deltas, delta)
	})
	ctx := core.WithCompactionProgress(context.Background(), progressFn)

	result, err := GenerateSummary(ctx, registry, nil, model, 4096, "key", "", "")
	if err != nil {
		t.Fatalf("GenerateSummary: %v", err)
	}
	if result != part1+part2 {
		t.Errorf("expected %q, got %q", part1+part2, result)
	}

	// The first call should be the "summarizing history" phase signal with empty delta.
	if len(phases) == 0 {
		t.Fatal("expected at least one progress call")
	}
	if phases[0] != "summarizing history" {
		t.Errorf("expected first phase %q, got %q", "summarizing history", phases[0])
	}
	if deltas[0] != "" {
		t.Errorf("expected first delta to be empty phase signal, got %q", deltas[0])
	}

	// Subsequent calls carry the incremental text.
	var accumulated string
	for i := 1; i < len(deltas); i++ {
		accumulated += deltas[i]
	}
	if accumulated != part1+part2 {
		t.Errorf("expected accumulated deltas %q, got %q", part1+part2, accumulated)
	}
}
