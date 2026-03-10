package compaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/config"
	firlog "github.com/kfet/fir/pkg/log"
)

// DefaultRunner implements core.CompactionRunner using the compaction package.
type DefaultRunner struct {
	SettingsManager *config.SettingsManager
	ModelRegistry   *models.ModelRegistry
}

// IsEnabled reports whether auto-compaction is enabled in settings.
func (r *DefaultRunner) IsEnabled() bool {
	return r.compactionSettings().Enabled
}

// ShouldCompact checks if compaction should trigger based on token counts.
func (r *DefaultRunner) ShouldCompact(contextTokens, contextWindow int, inputCostPerMTok float64) bool {
	settings := r.compactionSettings()
	return ShouldCompact(contextTokens, contextWindow, settings, inputCostPerMTok)
}

// GetStats returns pre-run information about what a compaction would process
// without running any LLM calls. Returns nil if there is nothing to compact.
func (r *DefaultRunner) GetStats(session *core.AgentSession) *core.CompactionInfo {
	settings := r.compactionSettings()
	pathEntries := session.SessionManager.GetBranch("")
	prep := PrepareCompaction(pathEntries, settings)
	if prep == nil {
		return nil
	}
	return &core.CompactionInfo{
		MessagesToSummarize: len(prep.MessagesToSummarize),
		TokensBefore:        prep.TokensBefore,
	}
}

// RunCompaction performs compaction using the compaction package.
func (r *DefaultRunner) RunCompaction(ctx context.Context, session *core.AgentSession, customInstructions string) (*core.CompactionResultInfo, error) {
	model := session.Model()
	if model == nil {
		return nil, fmt.Errorf("no model selected")
	}

	apiKey := r.ModelRegistry.GetApiKey(model)
	if apiKey == "" {
		return nil, fmt.Errorf("no API key for %s", model.Provider)
	}

	settings := r.compactionSettings()
	pathEntries := session.SessionManager.GetBranch("")
	firlog.Info("compaction starting", "entries", len(pathEntries), "model", model.ID)

	preparation := PrepareCompaction(pathEntries, settings)
	if preparation == nil {
		// Check why we can't compact
		if len(pathEntries) > 0 && pathEntries[len(pathEntries)-1].Type == "compaction" {
			return nil, fmt.Errorf("already compacted")
		}
		return nil, fmt.Errorf("nothing to compact (session too small)")
	}

	result, err := Compact(
		ctx,
		ai.DefaultRegistry,
		preparation,
		model,
		apiKey,
		customInstructions,
	)
	if err != nil {
		return nil, err
	}

	// Persist the compaction entry
	var detailsJSON json.RawMessage
	if result.Details != nil {
		detailsJSON, _ = json.Marshal(result.Details)
	}
	session.SessionManager.AppendCompaction(
		result.Summary,
		result.FirstKeptEntryID,
		result.TokensBefore,
		detailsJSON,
		false,
	)

	// Rebuild messages from compacted session
	sessionCtx := session.SessionManager.BuildSessionContext()
	session.Agent.ReplaceMessages(sessionCtx.Messages)

	firlog.Info("compaction complete", "tokensBefore", result.TokensBefore)

	return &core.CompactionResultInfo{
		Summary:          result.Summary,
		FirstKeptEntryID: result.FirstKeptEntryID,
		TokensBefore:     result.TokensBefore,
	}, nil
}

func (r *DefaultRunner) compactionSettings() CompactionSettings {
	s := r.SettingsManager.GetCompactionSettings()
	cs := CompactionSettings{
		Enabled:          s.Enabled,
		ReserveTokens:    s.ReserveTokens,
		KeepRecentTokens: s.KeepRecentTokens,
	}
	if s.FillRatio != nil {
		cs.FillRatio = *s.FillRatio
	}
	if s.MaxContextTokens != nil {
		cs.MaxContextTokens = *s.MaxContextTokens
	}
	if s.MaxContextCost != nil {
		cs.MaxContextCost = *s.MaxContextCost
	}
	return cs
}
