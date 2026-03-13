// Ported from: packages/coding-agent/src/modes/interactive/components/keybinding-hints.ts
// Upstream hash: 9f2c4a1b
package components

import (
	"strings"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// formatKeys formats a key array as display string (e.g. ["ctrl+c", "escape"] -> "ctrl+c/escape").
func formatKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return strings.Join(keys, "/")
}

// EditorKey returns the display string for an editor action.
func EditorKey(action tuicomp.EditorAction) string {
	keys := tuicomp.GetEditorKeys(action)
	strs := make([]string, len(keys))
	for i, k := range keys {
		strs[i] = string(k)
	}
	return formatKeys(strs)
}

// AppKey returns the display string for an app action.
func AppKey(keybindings *tui.KeybindingsManager, action tui.AppAction) string {
	return formatKeys(keybindings.GetKeys(action))
}

// KeyHint formats a keybinding hint with consistent styling: dim key, muted description.
func KeyHint(action tuicomp.EditorAction, description string) string {
	t := theme.GetTheme()
	return t.Fg("dim", EditorKey(action)) + t.Fg("muted", " "+description)
}

// AppKeyHint formats a keybinding hint for app-level actions.
func AppKeyHint(keybindings *tui.KeybindingsManager, action tui.AppAction, description string) string {
	t := theme.GetTheme()
	return t.Fg("dim", AppKey(keybindings, action)) + t.Fg("muted", " "+description)
}

// RawKeyHint formats a raw key string with description (for non-configurable keys like ↑↓).
func RawKeyHint(key, description string) string {
	t := theme.GetTheme()
	return t.Fg("dim", key) + t.Fg("muted", " "+description)
}
