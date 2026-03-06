package components

import (
	"strings"
	"testing"
)

func defaultSettingsConfig() SettingsConfig {
	return SettingsConfig{
		AutoCompactMode:         "client",
		ShowImages:              true,
		AutoResizeImages:        true,
		BlockImages:             false,
		EnableSkillCommands:     true,
		SteeringMode:            "one-at-a-time",
		FollowUpMode:            "one-at-a-time",
		ThinkingLevel:           "medium",
		AvailableThinkingLevels: []string{"off", "minimal", "low", "medium", "high"},
		CurrentTheme:            "dark",
		AvailableThemes:         []string{"dark", "light"},
		HideThinkingBlock:       false,
		CollapseChangelog:       false,
		DoubleEscapeAction:      "tree",
		ShowHardwareCursor:      false,
		EditorPaddingX:          1,
		AutocompleteMaxVisible:  10,
		QuietStartup:            false,
		ClearOnShrink:           false,
	}
}

func TestSettingsSelectorComponent_Render(t *testing.T) {
	comp := NewSettingsSelectorComponent(defaultSettingsConfig(), SettingsCallbacks{})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Settings") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Settings' in render output")
	}
}

func TestSettingsSelectorComponent_Navigation(t *testing.T) {
	comp := NewSettingsSelectorComponent(defaultSettingsConfig(), SettingsCallbacks{})
	if comp.selectedIndex != 0 {
		t.Errorf("expected initial index 0, got %d", comp.selectedIndex)
	}

	// Move down
	comp.HandleInput("\x1b[B") // down arrow
	if comp.selectedIndex != 1 {
		t.Errorf("expected index 1 after down, got %d", comp.selectedIndex)
	}

	// Move up
	comp.HandleInput("\x1b[A") // up arrow
	if comp.selectedIndex != 0 {
		t.Errorf("expected index 0 after up, got %d", comp.selectedIndex)
	}
}

func TestSettingsSelectorComponent_CycleValue(t *testing.T) {
	changed := false
	comp := NewSettingsSelectorComponent(defaultSettingsConfig(), SettingsCallbacks{
		OnAutoCompactModeChange: func(v string) {
			changed = true
			if v != "server" {
				t.Errorf("expected server, got %v", v)
			}
		},
	})

	// First entry is autocompact, currently "client"
	comp.selectedIndex = 0
	comp.HandleInput("\x1b[C") // right arrow cycles
	if !changed {
		t.Error("expected OnAutoCompactModeChange to be called")
	}
}

func TestSettingsSelectorComponent_Cancel(t *testing.T) {
	cancelled := false
	comp := NewSettingsSelectorComponent(defaultSettingsConfig(), SettingsCallbacks{
		OnCancel: func() { cancelled = true },
	})
	comp.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected OnCancel to be called")
	}
}

func TestBuildSettingsEntries(t *testing.T) {
	entries := buildSettingsEntries(defaultSettingsConfig())
	if len(entries) == 0 {
		t.Fatal("expected non-empty entries")
	}

	// Check that autocompact is first
	if entries[0].ID != "autocompact" {
		t.Errorf("expected first entry autocompact, got %s", entries[0].ID)
	}

	// Check thinking level has correct values
	var thinkingEntry *settingEntry
	for i := range entries {
		if entries[i].ID == "thinking" {
			thinkingEntry = &entries[i]
			break
		}
	}
	if thinkingEntry == nil {
		t.Fatal("expected thinking entry")
	}
	if thinkingEntry.CurrentValue != "medium" {
		t.Errorf("expected thinking level 'medium', got %s", thinkingEntry.CurrentValue)
	}
}
