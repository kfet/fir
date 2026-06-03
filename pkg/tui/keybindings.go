// Ported from: packages/coding-agent/src/core/keybindings.ts
// Upstream hash: 1caadb2e
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppAction is an application-level action.
type AppAction string

const (
	ActionInterrupt          AppAction = "interrupt"
	ActionClear              AppAction = "clear"
	ActionExit               AppAction = "exit"
	ActionSuspend            AppAction = "suspend"
	ActionCycleThinkingLevel AppAction = "cycleThinkingLevel"
	ActionCycleModelForward  AppAction = "cycleModelForward"
	ActionCycleModelBackward AppAction = "cycleModelBackward"
	ActionSelectModel        AppAction = "selectModel"
	ActionExpandTools        AppAction = "expandTools"
	ActionToggleThinking     AppAction = "toggleThinking"
	ActionExternalEditor     AppAction = "externalEditor"
	ActionFollowUp           AppAction = "followUp"
	ActionDequeue            AppAction = "dequeue"
	ActionPasteImage         AppAction = "pasteImage"
	ActionSelectThinking     AppAction = "selectThinking"
	ActionNewSession         AppAction = "newSession"
	ActionTree               AppAction = "tree"
	ActionResume             AppAction = "resume"
	ActionTogglePlan         AppAction = "togglePlan"
	ActionShowSession        AppAction = "showSession"
	ActionDismissAside       AppAction = "dismissAside"
)

// DefaultAppKeybindings maps app actions to their default key bindings.
var DefaultAppKeybindings = map[AppAction][]string{
	ActionInterrupt:          {"escape"},
	ActionClear:              {"ctrl+c"},
	ActionExit:               {"ctrl+d"},
	ActionSuspend:            {"ctrl+z"},
	ActionCycleThinkingLevel: {"shift+tab"},
	ActionCycleModelForward:  {"ctrl+p"},
	ActionCycleModelBackward: {"shift+ctrl+p"},
	ActionSelectModel:        {"ctrl+l"},
	ActionExpandTools:        {"ctrl+o"},
	ActionToggleThinking:     {"ctrl+t"},
	ActionExternalEditor:     {"ctrl+g"},
	ActionFollowUp:           {"alt+enter"},
	ActionDequeue:            {"alt+up"},
	ActionPasteImage:         {"ctrl+v"},
	ActionNewSession:         {"ctrl+n"},
	ActionSelectThinking:     {},
	ActionTree:               {},
	ActionResume:             {},
	ActionTogglePlan:         {"ctrl+r"},
	ActionShowSession:        {"ctrl+s"},
	ActionDismissAside:       {"alt+a"},
}

// KeybindingsConfig is the JSON config mapping actions to key IDs.
type KeybindingsConfig map[string]any // action → string or []string

// KeybindingsManager manages app keybindings.
type KeybindingsManager struct {
	config        KeybindingsConfig
	appActionKeys map[AppAction][]string
}

// NewKeybindingsManager creates a keybindings manager that loads from the
// global agentDir and optionally merges project-level overrides from projectDir.
// Project-level keybindings take precedence over global ones.
func NewKeybindingsManager(agentDir string, projectDirs ...string) *KeybindingsManager {
	config := loadKeybindingsFile(filepath.Join(agentDir, "keybindings.json"))

	// Merge project-level keybindings (last wins).
	for _, dir := range projectDirs {
		if dir == "" {
			continue
		}
		projectConfig := loadKeybindingsFile(filepath.Join(dir, "keybindings.json"))
		for k, v := range projectConfig {
			config[k] = v
		}
	}

	m := &KeybindingsManager{
		config:        config,
		appActionKeys: make(map[AppAction][]string),
	}
	m.buildMaps()
	return m
}

// NewKeybindingsManagerInMemory creates a keybindings manager with in-memory config.
func NewKeybindingsManagerInMemory(config KeybindingsConfig) *KeybindingsManager {
	if config == nil {
		config = KeybindingsConfig{}
	}
	m := &KeybindingsManager{
		config:        config,
		appActionKeys: make(map[AppAction][]string),
	}
	m.buildMaps()
	return m
}

func loadKeybindingsFile(path string) KeybindingsConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return KeybindingsConfig{}
	}
	var config KeybindingsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return KeybindingsConfig{}
	}
	return config
}

func (m *KeybindingsManager) buildMaps() {
	// Start with defaults
	for action, keys := range DefaultAppKeybindings {
		m.appActionKeys[action] = append([]string{}, keys...)
	}

	// Override with user config
	for action, keys := range m.config {
		appAction := AppAction(action)
		if !isAppAction(appAction) {
			continue
		}
		switch v := keys.(type) {
		case string:
			m.appActionKeys[appAction] = []string{v}
		case []any:
			var keyStrs []string
			for _, k := range v {
				if s, ok := k.(string); ok {
					keyStrs = append(keyStrs, s)
				}
			}
			m.appActionKeys[appAction] = keyStrs
		}
	}
}

// GetKeys returns the key bindings for an app action.
func (m *KeybindingsManager) GetKeys(action AppAction) []string {
	return m.appActionKeys[action]
}

// Matches checks if terminal input data matches an app action.
func (m *KeybindingsManager) Matches(data string, action AppAction) bool {
	for _, key := range m.appActionKeys[action] {
		if MatchesKey(data, KeyID(key)) {
			return true
		}
	}
	return false
}

func isAppAction(action AppAction) bool {
	_, ok := DefaultAppKeybindings[action]
	return ok
}
