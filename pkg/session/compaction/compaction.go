// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"context"
	"encoding/json"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// CompactionDetails are stored in CompactionEntry.Details for file tracking.
type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactionSettings controls compaction behavior.
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
	MaxContextTokens int  `json:"maxContextTokens"` // absolute token cap; 0 = disabled
}

// DefaultCompactionSettings are the default compaction settings.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:          true,
	ReserveTokens:    16384,
	KeepRecentTokens: 20000,
}

// CompactionResult is the result of a compaction operation.
type CompactionResult struct {
	Summary          string `json:"summary"`
	FirstKeptEntryID string `json:"firstKeptEntryId"`
	TokensBefore     int    `json:"tokensBefore"`
	Details          any    `json:"details,omitempty"`
}

// CompactionPreparation contains pre-calculated data for compaction.
type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []agent.AgentMessage
	TurnPrefixMessages  []agent.AgentMessage
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
	FileOps             *FileOperations
	Settings            CompactionSettings
}

// CutPointResult describes where to cut the session for compaction.
type CutPointResult struct {
	FirstKeptEntryIndex int
	TurnStartIndex      int
	IsSplitTurn         bool
}

// ContextUsageEstimate holds token estimation results.
type ContextUsageEstimate struct {
	Tokens         int
	UsageTokens    int
	TrailingTokens int
	LastUsageIndex *int
}

// ============================================================================
// File operation extraction
// ============================================================================

func extractFileOperations(messages []agent.AgentMessage, entries []*store.SessionEntry, prevCompactionIndex int) *FileOperations {
	fileOps := NewFileOperations()

	if prevCompactionIndex >= 0 {
		prev := entries[prevCompactionIndex]
		if prev.Type == "compaction" && !prev.FromHook && len(prev.Details) > 0 {
			var details CompactionDetails
			if err := json.Unmarshal(prev.Details, &details); err == nil {
				for _, f := range details.ReadFiles {
					fileOps.Read[f] = struct{}{}
				}
				for _, f := range details.ModifiedFiles {
					fileOps.Edited[f] = struct{}{}
				}
			}
		}
	}

	for _, msg := range messages {
		ExtractFileOpsFromMessage(msg, fileOps)
	}

	return fileOps
}

// ============================================================================
// Compaction preparation
// ============================================================================

// PrepareCompaction prepares compaction data from session entries.
func PrepareCompaction(pathEntries []*store.SessionEntry, settings CompactionSettings) *CompactionPreparation {
	if len(pathEntries) == 0 {
		return nil
	}
	if len(pathEntries) > 0 && pathEntries[len(pathEntries)-1].Type == "compaction" {
		return nil
	}

	prevCompactionIndex := -1
	for i := len(pathEntries) - 1; i >= 0; i-- {
		if pathEntries[i].Type == "compaction" {
			prevCompactionIndex = i
			break
		}
	}

	boundaryStart := prevCompactionIndex + 1
	boundaryEnd := len(pathEntries)

	usageStart := prevCompactionIndex
	if usageStart < 0 {
		usageStart = 0
	}
	var usageMessages []agent.AgentMessage
	for i := usageStart; i < boundaryEnd; i++ {
		if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
			usageMessages = append(usageMessages, *msg)
		}
	}
	tokensBefore := EstimateContextTokens(usageMessages).Tokens

	cutPoint := FindCutPoint(pathEntries, boundaryStart, boundaryEnd, settings.KeepRecentTokens)

	firstKeptEntry := pathEntries[cutPoint.FirstKeptEntryIndex]
	if firstKeptEntry.ID == "" {
		return nil
	}

	historyEnd := cutPoint.FirstKeptEntryIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}

	var messagesToSummarize []agent.AgentMessage
	for i := boundaryStart; i < historyEnd; i++ {
		if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
			messagesToSummarize = append(messagesToSummarize, *msg)
		}
	}

	var turnPrefixMessages []agent.AgentMessage
	if cutPoint.IsSplitTurn {
		for i := cutPoint.TurnStartIndex; i < cutPoint.FirstKeptEntryIndex; i++ {
			if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
				turnPrefixMessages = append(turnPrefixMessages, *msg)
			}
		}
	}

	var previousSummary string
	if prevCompactionIndex >= 0 {
		previousSummary = pathEntries[prevCompactionIndex].Summary
	}

	fileOps := extractFileOperations(messagesToSummarize, pathEntries, prevCompactionIndex)
	if cutPoint.IsSplitTurn {
		for _, msg := range turnPrefixMessages {
			ExtractFileOpsFromMessage(msg, fileOps)
		}
	}

	return &CompactionPreparation{
		FirstKeptEntryID:    firstKeptEntry.ID,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefixMessages,
		IsSplitTurn:         cutPoint.IsSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            settings,
	}
}

// ============================================================================
// Main compaction function
// ============================================================================

// Compact generates summaries for compaction using prepared data.
func Compact(
	ctx context.Context,
	registry *ai.Registry,
	preparation *CompactionPreparation,
	model *ai.Model,
	apiKey string,
	customInstructions string,
) (*CompactionResult, error) {
	var summary string

	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		var historyResult string
		var err error
		if len(preparation.MessagesToSummarize) > 0 {
			historyResult, err = GenerateSummary(
				ctx, registry, preparation.MessagesToSummarize, model,
				preparation.Settings.ReserveTokens, apiKey,
				customInstructions, preparation.PreviousSummary,
			)
			if err != nil {
				return nil, err
			}
		} else {
			historyResult = "No prior history."
		}

		turnPrefixResult, err := generateTurnPrefixSummary(
			ctx, registry, preparation.TurnPrefixMessages, model,
			preparation.Settings.ReserveTokens, apiKey,
		)
		if err != nil {
			return nil, err
		}

		summary = historyResult + "\n\n---\n\n**Turn Context (split turn):**\n\n" + turnPrefixResult
	} else {
		var err error
		summary, err = GenerateSummary(
			ctx, registry, preparation.MessagesToSummarize, model,
			preparation.Settings.ReserveTokens, apiKey,
			customInstructions, preparation.PreviousSummary,
		)
		if err != nil {
			return nil, err
		}
	}

	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	return &CompactionResult{
		Summary:          summary,
		FirstKeptEntryID: preparation.FirstKeptEntryID,
		TokensBefore:     preparation.TokensBefore,
		Details:          &CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}
