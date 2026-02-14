// Ported from: packages/coding-agent/src/modes/interactive/components/settings-selector.ts
// Upstream hash: c2d3e4f5
package components

import (
	"fmt"
	"strconv"

	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// ThinkingDescriptions maps thinking levels to human descriptions.
var ThinkingDescriptions = map[string]string{
	"off":     "No reasoning",
	"minimal": "Very brief reasoning (~1k tokens)",
	"low":     "Light reasoning (~2k tokens)",
	"medium":  "Moderate reasoning (~8k tokens)",
	"high":    "Deep reasoning (~16k tokens)",
	"xhigh":  "Maximum reasoning (~32k tokens)",
}

// SettingsConfig holds all settings values for the selector.
type SettingsConfig struct {
	AutoCompact            bool
	ShowImages             bool
	AutoResizeImages       bool
	BlockImages            bool
	EnableSkillCommands    bool
	SteeringMode           string // "all" or "one-at-a-time"
	FollowUpMode           string // "all" or "one-at-a-time"
	Transport              string // "sse", "websocket", or "auto"
	ThinkingLevel          string
	AvailableThinkingLevels []string
	CurrentTheme           string
	AvailableThemes        []string
	HideThinkingBlock      bool
	CollapseChangelog      bool
	DoubleEscapeAction     string // "fork", "tree", "none"
	ShowHardwareCursor     bool
	EditorPaddingX         int
	AutocompleteMaxVisible int
	QuietStartup           bool
	ClearOnShrink          bool
}

// SettingsCallbacks holds callbacks for settings changes.
type SettingsCallbacks struct {
	OnAutoCompactChange            func(bool)
	OnShowImagesChange             func(bool)
	OnAutoResizeImagesChange       func(bool)
	OnBlockImagesChange            func(bool)
	OnEnableSkillCommandsChange    func(bool)
	OnSteeringModeChange           func(string)
	OnFollowUpModeChange           func(string)
	OnTransportChange              func(string)
	OnThinkingLevelChange          func(string)
	OnThemeChange                  func(string)
	OnThemePreview                 func(string)
	OnHideThinkingBlockChange      func(bool)
	OnCollapseChangelogChange      func(bool)
	OnDoubleEscapeActionChange     func(string)
	OnShowHardwareCursorChange     func(bool)
	OnEditorPaddingXChange         func(int)
	OnAutocompleteMaxVisibleChange func(int)
	OnQuietStartupChange           func(bool)
	OnClearOnShrinkChange          func(bool)
	OnCancel                       func()
}

// settingEntry is a single setting row.
type settingEntry struct {
	ID           string
	Label        string
	Description  string
	CurrentValue string
	Values       []string // If non-nil, inline toggle; if nil, opens submenu
}

// SettingsSelectorComponent renders a settings menu.
type SettingsSelectorComponent struct {
	tui.Container
	entries       []settingEntry
	selectedIndex int
	maxVisible    int
	config        SettingsConfig
	callbacks     SettingsCallbacks
}

// NewSettingsSelectorComponent creates a new settings selector.
func NewSettingsSelectorComponent(config SettingsConfig, callbacks SettingsCallbacks) *SettingsSelectorComponent {
	c := &SettingsSelectorComponent{
		config:     config,
		callbacks:  callbacks,
		maxVisible: 12,
	}

	c.entries = buildSettingsEntries(config)

	c.AddChild(NewDynamicBorder(nil))
	return c
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func buildSettingsEntries(config SettingsConfig) []settingEntry {
	entries := []settingEntry{
		{ID: "autocompact", Label: "Auto-compact", Description: "Automatically compact context when it gets too large", CurrentValue: boolStr(config.AutoCompact), Values: []string{"true", "false"}},
		{ID: "auto-resize-images", Label: "Auto-resize images", Description: "Resize large images to 2000x2000 max", CurrentValue: boolStr(config.AutoResizeImages), Values: []string{"true", "false"}},
		{ID: "block-images", Label: "Block images", Description: "Prevent images from being sent to LLM providers", CurrentValue: boolStr(config.BlockImages), Values: []string{"true", "false"}},
		{ID: "skill-commands", Label: "Skill commands", Description: "Register skills as /skill:name commands", CurrentValue: boolStr(config.EnableSkillCommands), Values: []string{"true", "false"}},
		{ID: "steering-mode", Label: "Steering mode", Description: "Steering message delivery mode", CurrentValue: config.SteeringMode, Values: []string{"one-at-a-time", "all"}},
		{ID: "follow-up-mode", Label: "Follow-up mode", Description: "Follow-up message delivery mode", CurrentValue: config.FollowUpMode, Values: []string{"one-at-a-time", "all"}},
		{ID: "transport", Label: "Transport", Description: "Preferred transport for providers that support multiple transports", CurrentValue: config.Transport, Values: []string{"sse", "websocket", "auto"}},
		{ID: "hide-thinking", Label: "Hide thinking", Description: "Hide thinking blocks in assistant responses", CurrentValue: boolStr(config.HideThinkingBlock), Values: []string{"true", "false"}},
		{ID: "collapse-changelog", Label: "Collapse changelog", Description: "Show condensed changelog after updates", CurrentValue: boolStr(config.CollapseChangelog), Values: []string{"true", "false"}},
		{ID: "quiet-startup", Label: "Quiet startup", Description: "Disable verbose printing at startup", CurrentValue: boolStr(config.QuietStartup), Values: []string{"true", "false"}},
		{ID: "double-escape-action", Label: "Double-escape action", Description: "Action when pressing Escape twice with empty editor", CurrentValue: config.DoubleEscapeAction, Values: []string{"tree", "fork", "none"}},
		{ID: "show-hardware-cursor", Label: "Show hardware cursor", Description: "Show terminal cursor for IME support", CurrentValue: boolStr(config.ShowHardwareCursor), Values: []string{"true", "false"}},
		{ID: "editor-padding", Label: "Editor padding", Description: "Horizontal padding for input editor (0-3)", CurrentValue: strconv.Itoa(config.EditorPaddingX), Values: []string{"0", "1", "2", "3"}},
		{ID: "autocomplete-max-visible", Label: "Autocomplete max items", Description: "Max visible items in autocomplete dropdown", CurrentValue: strconv.Itoa(config.AutocompleteMaxVisible), Values: []string{"3", "5", "7", "10", "15", "20"}},
		{ID: "clear-on-shrink", Label: "Clear on shrink", Description: "Clear empty rows when content shrinks", CurrentValue: boolStr(config.ClearOnShrink), Values: []string{"true", "false"}},
		{ID: "thinking", Label: "Thinking level", Description: "Reasoning depth for thinking-capable models", CurrentValue: config.ThinkingLevel, Values: config.AvailableThinkingLevels},
		{ID: "theme", Label: "Theme", Description: "Color theme for the interface", CurrentValue: config.CurrentTheme, Values: config.AvailableThemes},
	}
	return entries
}

// Render renders the settings list.
func (c *SettingsSelectorComponent) Render(width int) []string {
	t := theme.GetTheme()
	lines := []string{}

	// Border
	lines = append(lines, NewDynamicBorder(nil).Render(width)...)

	// Title
	lines = append(lines, t.Bold("Settings"))
	lines = append(lines, "")

	// Calculate visible range
	start := c.selectedIndex - c.maxVisible/2
	if start > len(c.entries)-c.maxVisible {
		start = len(c.entries) - c.maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + c.maxVisible
	if end > len(c.entries) {
		end = len(c.entries)
	}

	for i := start; i < end; i++ {
		e := c.entries[i]
		isSelected := i == c.selectedIndex

		cursor := "  "
		if isSelected {
			cursor = t.Fg("accent", "> ")
		}

		label := e.Label
		if isSelected {
			label = t.Bold(label)
		}
		value := t.Fg("dim", e.CurrentValue)

		line := fmt.Sprintf("%s%s: %s", cursor, label, value)
		lines = append(lines, tui.TruncateToWidth(line, width, "...", false))

		if isSelected {
			lines = append(lines, "    "+t.Fg("muted", e.Description))
		}
	}

	lines = append(lines, "")
	lines = append(lines, t.Fg("dim", "  ←/→ change · Enter select · Esc close"))

	// Bottom border
	lines = append(lines, NewDynamicBorder(nil).Render(width)...)

	return lines
}

// HandleInput processes keyboard input.
func (c *SettingsSelectorComponent) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		if c.selectedIndex > 0 {
			c.selectedIndex--
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		if c.selectedIndex < len(c.entries)-1 {
			c.selectedIndex++
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		if c.callbacks.OnCancel != nil {
			c.callbacks.OnCancel()
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) || data == " " {
		c.cycleValue(1)
	} else if data == "\x1b[C" { // right arrow
		c.cycleValue(1)
	} else if data == "\x1b[D" { // left arrow
		c.cycleValue(-1)
	}
}

func (c *SettingsSelectorComponent) cycleValue(dir int) {
	if c.selectedIndex < 0 || c.selectedIndex >= len(c.entries) {
		return
	}
	e := &c.entries[c.selectedIndex]
	if len(e.Values) == 0 {
		return
	}
	curIdx := -1
	for i, v := range e.Values {
		if v == e.CurrentValue {
			curIdx = i
			break
		}
	}
	if curIdx < 0 {
		curIdx = 0
	}
	newIdx := (curIdx + dir + len(e.Values)) % len(e.Values)
	e.CurrentValue = e.Values[newIdx]
	c.applyChange(e.ID, e.CurrentValue)
}

func (c *SettingsSelectorComponent) applyChange(id, value string) {
	cb := c.callbacks
	switch id {
	case "autocompact":
		if cb.OnAutoCompactChange != nil {
			cb.OnAutoCompactChange(value == "true")
		}
	case "auto-resize-images":
		if cb.OnAutoResizeImagesChange != nil {
			cb.OnAutoResizeImagesChange(value == "true")
		}
	case "block-images":
		if cb.OnBlockImagesChange != nil {
			cb.OnBlockImagesChange(value == "true")
		}
	case "skill-commands":
		if cb.OnEnableSkillCommandsChange != nil {
			cb.OnEnableSkillCommandsChange(value == "true")
		}
	case "steering-mode":
		if cb.OnSteeringModeChange != nil {
			cb.OnSteeringModeChange(value)
		}
	case "follow-up-mode":
		if cb.OnFollowUpModeChange != nil {
			cb.OnFollowUpModeChange(value)
		}
	case "transport":
		if cb.OnTransportChange != nil {
			cb.OnTransportChange(value)
		}
	case "hide-thinking":
		if cb.OnHideThinkingBlockChange != nil {
			cb.OnHideThinkingBlockChange(value == "true")
		}
	case "collapse-changelog":
		if cb.OnCollapseChangelogChange != nil {
			cb.OnCollapseChangelogChange(value == "true")
		}
	case "quiet-startup":
		if cb.OnQuietStartupChange != nil {
			cb.OnQuietStartupChange(value == "true")
		}
	case "double-escape-action":
		if cb.OnDoubleEscapeActionChange != nil {
			cb.OnDoubleEscapeActionChange(value)
		}
	case "show-hardware-cursor":
		if cb.OnShowHardwareCursorChange != nil {
			cb.OnShowHardwareCursorChange(value == "true")
		}
	case "editor-padding":
		if cb.OnEditorPaddingXChange != nil {
			v, _ := strconv.Atoi(value)
			cb.OnEditorPaddingXChange(v)
		}
	case "autocomplete-max-visible":
		if cb.OnAutocompleteMaxVisibleChange != nil {
			v, _ := strconv.Atoi(value)
			cb.OnAutocompleteMaxVisibleChange(v)
		}
	case "clear-on-shrink":
		if cb.OnClearOnShrinkChange != nil {
			cb.OnClearOnShrinkChange(value == "true")
		}
	case "thinking":
		if cb.OnThinkingLevelChange != nil {
			cb.OnThinkingLevelChange(value)
		}
	case "theme":
		if cb.OnThemeChange != nil {
			cb.OnThemeChange(value)
		}
	}
}

// SelectedEntry returns the currently selected entry.
func (c *SettingsSelectorComponent) SelectedEntry() *settingEntry {
	if c.selectedIndex >= 0 && c.selectedIndex < len(c.entries) {
		return &c.entries[c.selectedIndex]
	}
	return nil
}
