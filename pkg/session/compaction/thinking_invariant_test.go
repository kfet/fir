package compaction

// Compaction must never silently mutate a stored assistant turn that
// carries a signed/redacted thinking block. After a cut, the kept
// messages are forwarded verbatim to the LLM on the next turn; if a
// signed thinking block is mutated (or its sibling sequence is
// rewritten) Anthropic returns 400 "thinking or redacted_thinking
// blocks in the latest assistant message cannot be modified". This
// suite pins two invariants:
//
//  1. The latest assistant turn — which carries the live signed
//     thinking the next /messages call will replay verbatim — never
//     enters the summarizer's input. Summarizing it would force a
//     mismatch between the summary's flattened representation and the
//     signed thinking block fir is still required to send.
//
//  2. Split-turn compaction never produces a TurnPrefixMessages list
//     containing a fragment of an assistant message (e.g. just
//     thinking, no text/tool_use). Cut points operate at entry
//     granularity and each assistant turn is a single entry, so this
//     should hold by construction — pin it explicitly.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

func makeAssistantEntryWithThinking(id, thinkingText, signature, replyText, toolCallName string) *store.SessionEntry {
	thinking := ai.NewThinkingContent(thinkingText)
	thinking.Thinking.ThinkingSignature = signature
	content := []ai.AssistantContent{thinking, ai.NewTextContent(replyText)}
	if toolCallName != "" {
		content = append(content, ai.NewToolCallContent("tc_"+id, toolCallName, map[string]any{"x": id}))
	}
	am := ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Provider:   ai.ProviderAnthropic,
		API:        ai.ApiAnthropicMessages,
		Model:      "claude-opus-4-7",
		Content:    content,
		StopReason: ai.StopReasonToolUse,
	}
	raw, _ := json.Marshal(ai.NewAssistantMsg(am))
	return &store.SessionEntry{Type: "message", ID: id, RawMessage: raw}
}

// The latest assistant turn's signed thinking must never be summarized:
// the wire request still has to replay it verbatim, so a summarizer-
// flattened version would create a mismatch.
func TestPrepareCompaction_LatestSignedThinkingNotSummarized(t *testing.T) {
	entries := []*store.SessionEntry{
		makeUserEntry("u1", strings.Repeat("a", 400)),
		makeAssistantEntryWithThinking("a1", "First thought.", "sig-a1", "First reply.", "bash"),
		makeToolResultEntry("tr1"),
		makeUserEntry("u2", strings.Repeat("b", 400)),
		makeAssistantEntryWithThinking("a2", "Second thought.", "sig-a2", "Second reply.", "bash"),
		makeToolResultEntry("tr2"),
		makeUserEntry("u3", strings.Repeat("c", 400)),
		makeAssistantEntryWithThinking("a3", "Third thought.", "sig-a3", "Third reply.", ""),
	}
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    50,
		KeepRecentTokens: 30, // force aggressive cut
	}
	prep := PrepareCompaction(entries, settings)
	if prep == nil {
		t.Fatal("expected a preparation")
	}

	// FirstKeptEntryID must point at a real entry boundary (not a
	// synthesised cut between blocks of one assistant message).
	found := false
	for _, e := range entries {
		if e.ID == prep.FirstKeptEntryID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("FirstKeptEntryID %q does not match any entry", prep.FirstKeptEntryID)
	}

	// The latest assistant's signed thinking must not be summarized.
	const latestSig = "sig-a3"
	for _, m := range prep.MessagesToSummarize {
		a := m.AsAssistant()
		if a == nil {
			continue
		}
		for _, c := range a.Content {
			if c.IsThinking() && c.Thinking.ThinkingSignature == latestSig {
				t.Fatalf("latest assistant's signed thinking %q leaked into MessagesToSummarize — the wire request will replay it verbatim while the summarizer flattens it", latestSig)
			}
		}
	}
}

// Split-turn compaction never produces a TurnPrefixMessages entry that
// is only a fragment of an assistant message.
func TestPrepareCompaction_SplitTurn_AssistantStaysIntact(t *testing.T) {
	entries := []*store.SessionEntry{
		makeUserEntry("u1", strings.Repeat("x", 800)),
		makeAssistantEntryWithThinking("a1", "Old thought.", "sig-a1", "Old reply.", "bash"),
		makeToolResultEntry("tr1"),
		makeUserEntry("u2", strings.Repeat("a", 800)), // turn start
		makeAssistantEntryWithThinking("a2", "Split thought.", "sig-a2", "Split reply.", "bash"),
		makeToolResultEntry("tr2"),
		makeAssistantEntryWithThinking("a3", strings.Repeat("c", 800), "sig-a3", "After.", ""),
	}
	settings := CompactionSettings{
		Enabled:          true,
		ReserveTokens:    30,
		KeepRecentTokens: 50,
	}
	prep := PrepareCompaction(entries, settings)
	if prep == nil {
		t.Fatal("expected preparation")
	}
	if !prep.IsSplitTurn {
		t.Skip("scenario did not produce a split turn — invariant only applies to split turns")
	}

	for _, m := range prep.TurnPrefixMessages {
		a := m.AsAssistant()
		if a == nil {
			continue
		}
		var hasThinking, hasTextOrToolCall bool
		for _, c := range a.Content {
			switch {
			case c.IsThinking():
				hasThinking = true
				if c.Thinking.ThinkingSignature == "" && !c.Thinking.Redacted {
					t.Errorf("split-turn prefix lost a thinking signature: %+v", c.Thinking)
				}
			case c.IsText(), c.IsToolCall():
				hasTextOrToolCall = true
			}
		}
		if hasThinking && !hasTextOrToolCall {
			t.Errorf("split-turn prefix has assistant message with thinking but no text/tool_use — fragmented turn: %+v", a.Content)
		}
	}
}
