// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"encoding/json"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Cut point detection
// ============================================================================

func getMessageRole(entry *store.SessionEntry) string {
	if entry.Type != "message" || len(entry.RawMessage) == 0 {
		return ""
	}
	var probe struct {
		Role string `json:"role"`
	}
	json.Unmarshal(entry.RawMessage, &probe)
	return probe.Role
}

func findValidCutPoints(entries []*store.SessionEntry, startIndex, endIndex int) []int {
	var cutPoints []int
	for i := startIndex; i < endIndex; i++ {
		entry := entries[i]
		switch entry.Type {
		case "message":
			role := getMessageRole(entry)
			switch role {
			case "bashExecution", "custom", "branchSummary", "compactionSummary", "user", "assistant":
				cutPoints = append(cutPoints, i)
			}
		case "branch_summary", "custom_message":
			cutPoints = append(cutPoints, i)
		}
	}
	return cutPoints
}

// FindTurnStartIndex finds the user message that starts the turn containing the given entry.
func FindTurnStartIndex(entries []*store.SessionEntry, entryIndex, startIndex int) int {
	for i := entryIndex; i >= startIndex; i-- {
		entry := entries[i]
		if entry.Type == "branch_summary" || entry.Type == "custom_message" {
			return i
		}
		if entry.Type == "message" {
			role := getMessageRole(entry)
			if role == "user" || role == "bashExecution" {
				return i
			}
		}
	}
	return -1
}

// FindCutPoint finds where to cut the session for compaction.
func FindCutPoint(entries []*store.SessionEntry, startIndex, endIndex, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(entries, startIndex, endIndex)

	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}

	accumulatedTokens := 0
	cutIndex := cutPoints[0]

	for i := endIndex - 1; i >= startIndex; i-- {
		entry := entries[i]
		if entry.Type != "message" || len(entry.RawMessage) == 0 {
			continue
		}
		var msg ai.Message
		if err := json.Unmarshal(entry.RawMessage, &msg); err != nil {
			continue
		}
		messageTokens := EstimateTokens(agent.NewAgentMessage(msg))
		accumulatedTokens += messageTokens

		if accumulatedTokens >= keepRecentTokens {
			for c := 0; c < len(cutPoints); c++ {
				if cutPoints[c] >= i {
					cutIndex = cutPoints[c]
					break
				}
			}
			break
		}
	}

	// Scan backwards to include non-message entries
	for cutIndex > startIndex {
		prev := entries[cutIndex-1]
		if prev.Type == "compaction" || prev.Type == "message" {
			break
		}
		cutIndex--
	}

	isUserMsg := getMessageRole(entries[cutIndex]) == "user"
	turnStartIndex := -1
	if !isUserMsg {
		turnStartIndex = FindTurnStartIndex(entries, cutIndex, startIndex)
	}

	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStartIndex,
		IsSplitTurn:         !isUserMsg && turnStartIndex != -1,
	}
}

// ============================================================================
// Message extraction from entries
// ============================================================================

func getMessageFromEntry(entry *store.SessionEntry) *agent.AgentMessage {
	switch entry.Type {
	case "message":
		if len(entry.RawMessage) == 0 {
			return nil
		}
		var msg ai.Message
		if err := json.Unmarshal(entry.RawMessage, &msg); err != nil {
			return nil
		}
		am := agent.NewAgentMessage(msg)
		return &am

	case "custom_message":
		if len(entry.Content) > 0 && entry.CustomType != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			var content any
			json.Unmarshal(entry.Content, &content)
			cm := &store.CustomMessage{
				Role:       "custom",
				CustomType: entry.CustomType,
				Content:    content,
				Display:    entry.Display,
				Timestamp:  ts.UnixMilli(),
			}
			am := agent.AgentMessage{Custom: cm}
			return &am
		}

	case "branch_summary":
		if entry.Summary != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			am := store.CreateBranchSummaryMessage(entry.Summary, entry.FromID, ts)
			return &am
		}

	case "compaction":
		if entry.Summary != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			am := store.CreateCompactionSummaryMessage(entry.Summary, entry.TokensBefore, ts)
			return &am
		}
	}

	return nil
}
