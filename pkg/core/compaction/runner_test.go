package compaction

import (
	"testing"

	"github.com/kfet/pi-go/pkg/core"
)

func TestDefaultRunner_ShouldCompact(t *testing.T) {
	sm := core.NewInMemorySettingsManager(core.Settings{})
	runner := &DefaultRunner{
		SettingsManager: sm,
	}

	// Default settings: reserveTokens=16384
	// Should compact when contextTokens > contextWindow - reserveTokens
	if runner.ShouldCompact(50000, 200000) {
		t.Error("should not compact when well under threshold")
	}

	// contextWindow=200000, reserve=16384 → threshold=183616
	if !runner.ShouldCompact(190000, 200000) {
		t.Error("should compact when over threshold")
	}
}

func TestDefaultRunner_ShouldCompact_Disabled(t *testing.T) {
	enabled := false
	sm := core.NewInMemorySettingsManager(core.Settings{
		Compaction: &core.CompactionSettings{
			Enabled: &enabled,
		},
	})
	runner := &DefaultRunner{
		SettingsManager: sm,
	}

	if runner.ShouldCompact(190000, 200000) {
		t.Error("should not compact when disabled")
	}
}
