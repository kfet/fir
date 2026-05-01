package session

import (
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

func TestCompactionArtifacts_Empty(t *testing.T) {
	sess, _ := newTestAgentSession(t)
	defer sess.Close()

	arts, prevSummary, prevIdx := sess.CompactionArtifacts()
	if len(arts) != 0 {
		t.Errorf("expected 0 artifacts on empty session, got %d", len(arts))
	}
	if prevSummary != "" {
		t.Errorf("expected empty prevSummary, got %q", prevSummary)
	}
	if prevIdx != -1 {
		t.Errorf("expected prevIdx=-1, got %d", prevIdx)
	}
}

func TestCompactionArtifacts_BasicShape(t *testing.T) {
	sess, _ := newTestAgentSession(t)
	defer sess.Close()

	now := time.Now().UnixMilli()
	sess.SessionStore.AppendAIMessage(ai.NewUserMsg("hello", now))
	sess.SessionStore.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("hi")},
	}))

	arts, prevSummary, prevIdx := sess.CompactionArtifacts()
	if len(arts) < 2 {
		t.Fatalf("expected at least 2 artifacts, got %d", len(arts))
	}
	if arts[0].Kind != ArtifactUser {
		t.Errorf("expected first artifact User, got %v", arts[0].Kind)
	}
	if arts[0].EntryID == "" {
		t.Errorf("expected non-empty EntryID")
	}
	if arts[1].Kind != ArtifactAssistant {
		t.Errorf("expected second artifact Assistant, got %v", arts[1].Kind)
	}
	if prevSummary != "" || prevIdx != -1 {
		t.Errorf("no prior compaction expected; got summary=%q idx=%d", prevSummary, prevIdx)
	}
}

func TestApplyCompaction_PersistsAndRebuilds(t *testing.T) {
	sess, _ := newTestAgentSession(t)
	defer sess.Close()

	now := time.Now().UnixMilli()
	sess.SessionStore.AppendAIMessage(ai.NewUserMsg("first", now))
	sess.SessionStore.AppendAIMessage(ai.NewUserMsg("second", now))

	entries := sess.SessionStore.GetBranch("")
	firstID := entries[0].ID

	if err := sess.ApplyCompaction(CompactionOutput{
		Summary:          "test summary",
		FirstKeptEntryID: firstID,
		TokensBefore:     100,
	}); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}

	_, prevSummary, prevIdx := sess.CompactionArtifacts()
	if prevSummary != "test summary" {
		t.Errorf("expected prevSummary='test summary', got %q", prevSummary)
	}
	if prevIdx < 0 {
		t.Errorf("expected prevIdx >= 0, got %d", prevIdx)
	}
}

// TestApplyCompaction_RebuildIncludesRecallHint asserts the rebuilt
// session context surfaces the recall hint attached to
// CompactionSummarySuffix — the continuing agent must see it adjacent
// to any (entry <id>) / [entry <id> ...] references in the summary.
func TestApplyCompaction_RebuildIncludesRecallHint(t *testing.T) {
	sess, _ := newTestAgentSession(t)
	defer sess.Close()

	now := time.Now().UnixMilli()
	sess.SessionStore.AppendAIMessage(ai.NewUserMsg("hello", now))
	entries := sess.SessionStore.GetBranch("")
	firstID := entries[0].ID

	if err := sess.ApplyCompaction(CompactionOutput{
		Summary:          "summary body",
		FirstKeptEntryID: firstID,
		TokensBefore:     1,
	}); err != nil {
		t.Fatalf("ApplyCompaction: %v", err)
	}

	// Rebuild the LLM context. The compaction summary lives as a Custom
	// agent message at this layer; converting via store.ConvertToLLM
	// reifies it into the user-message wrapper that includes Prefix +
	// Summary + Suffix (the recall hint).
	ctx := sess.SessionStore.BuildSessionContext()
	llm, err := store.ConvertToLLM(ctx.Messages)
	if err != nil {
		t.Fatalf("ConvertToLLM: %v", err)
	}
	var found bool
	for _, m := range llm {
		u := m.AsUser()
		if u == nil {
			continue
		}
		text, _ := u.Content.(string)
		if strings.Contains(text, "summary body") && strings.Contains(text, "Note on references in the summary above") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected post-compaction LLM context to contain summary body alongside the recall hint")
	}
}
