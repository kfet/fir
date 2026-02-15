// Ported from: packages/coding-agent/src/modes/interactive/components/custom-editor.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// CustomEditor wraps the Editor with app-level keybinding support.
type CustomEditor struct {
	*tuicomp.Editor
	keybindings    *core.KeybindingsManager
	actionHandlers map[core.AppAction]func()

	// Dynamic handlers that can be replaced
	OnEscape            func()
	OnCtrlD             func()
	OnPasteImage        func()
	OnExtensionShortcut func(data string) bool
}

// NewCustomEditor creates a new CustomEditor.
func NewCustomEditor(t *tui.TUI, theme tuicomp.EditorTheme, keybindings *core.KeybindingsManager, opts ...tuicomp.EditorOptions) *CustomEditor {
	return &CustomEditor{
		Editor:         tuicomp.NewEditor(t, theme, opts...),
		keybindings:    keybindings,
		actionHandlers: make(map[core.AppAction]func()),
	}
}

// OnAction registers a handler for an app action.
func (ce *CustomEditor) OnAction(action core.AppAction, handler func()) {
	ce.actionHandlers[action] = handler
}

// HandleInput processes keyboard input, checking app keybindings first.
func (ce *CustomEditor) HandleInput(data string) {
	// Check extension shortcuts first
	if ce.OnExtensionShortcut != nil && ce.OnExtensionShortcut(data) {
		return
	}

	// Check paste image
	if ce.keybindings.Matches(data, core.ActionPasteImage) {
		if ce.OnPasteImage != nil {
			ce.OnPasteImage()
		}
		return
	}

	// Escape/interrupt — only if autocomplete is NOT active
	if ce.keybindings.Matches(data, core.ActionInterrupt) {
		if !ce.IsShowingAutocomplete() {
			handler := ce.OnEscape
			if handler == nil {
				handler = ce.actionHandlers[core.ActionInterrupt]
			}
			if handler != nil {
				handler()
				return
			}
		}
		// Let parent handle escape for autocomplete cancellation
		ce.Editor.HandleInput(data)
		return
	}

	// Exit (Ctrl+D) — only when editor is empty
	if ce.keybindings.Matches(data, core.ActionExit) {
		if ce.GetText() == "" {
			handler := ce.OnCtrlD
			if handler == nil {
				handler = ce.actionHandlers[core.ActionExit]
			}
			if handler != nil {
				handler()
			}
			return
		}
		// Fall through to editor handling for delete-char-forward when not empty
	}

	// Check all other app actions
	for action, handler := range ce.actionHandlers {
		if action != core.ActionInterrupt && action != core.ActionExit && ce.keybindings.Matches(data, action) {
			handler()
			return
		}
	}

	// Pass to parent for editor handling
	ce.Editor.HandleInput(data)
}
