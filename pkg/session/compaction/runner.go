package compaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/session"
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
func (r *DefaultRunner) ShouldCompact(contextTokens, contextWindow int) bool {
	settings := r.compactionSettings()
	return ShouldCompact(contextTokens, contextWindow, settings)
}

// GetStats returns pre-run information about what a compaction would process
// without running any LLM calls. Returns nil if there is nothing to compact.
func (r *DefaultRunner) GetStats(sess *session.AgentSession) *session.CompactionInfo {
	settings := r.compactionSettings()
	pathEntries := sess.SessionStore.GetBranch("")
	prep := PrepareCompaction(pathEntries, settings)
	if prep == nil {
		return nil
	}
	return &session.CompactionInfo{
		MessagesToSummarize: len(prep.MessagesToSummarize),
		TokensBefore:        prep.TokensBefore,
	}
}

// RunCompaction performs compaction using the compaction package.
func (r *DefaultRunner) RunCompaction(ctx context.Context, sess *session.AgentSession, customInstructions string) (*session.CompactionResultInfo, error) {
	model := sess.Model()
	if model == nil {
		return nil, fmt.Errorf("no model selected")
	}

	apiKey := r.ModelRegistry.GetApiKey(model)
	if apiKey == "" {
		detail := ""
		if err := r.ModelRegistry.GetApiKeyError(model.Provider); err != nil {
			detail = err.Error()
		}
		if detail != "" {
			return nil, fmt.Errorf("no API key for %s: %s", model.Provider, detail)
		}
		return nil, fmt.Errorf("no API key for %s. Set an API key or run 'fir login %s'", model.Provider, model.Provider)
	}

	settings := r.compactionSettings()
	pathEntries := sess.SessionStore.GetBranch("")
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

	// Persist the compaction entry and rebuild the agent's in-memory
	// message list via the session-level write path.
	var detailsJSON json.RawMessage
	if result.Details != nil {
		detailsJSON, _ = json.Marshal(result.Details)
	}
	if err := sess.ApplyCompaction(session.CompactionOutput{
		Summary:          result.Summary,
		FirstKeptEntryID: result.FirstKeptEntryID,
		TokensBefore:     result.TokensBefore,
		DetailsJSON:      detailsJSON,
		FromHook:         false,
	}); err != nil {
		return nil, fmt.Errorf("apply compaction: %w", err)
	}

	firlog.Info("compaction complete", "tokensBefore", result.TokensBefore)

	return &session.CompactionResultInfo{
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
	if s.MaxContextTokens != nil {
		cs.MaxContextTokens = *s.MaxContextTokens
	}
	return cs
}
