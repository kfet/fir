// Ported from: packages/coding-agent/src/modes/interactive/components/custom-editor.ts
// Upstream hash: 1caadb2e
package components

import (
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

// CustomEditor wraps the Editor with app-level keybinding support.
type CustomEditor struct {
	*tuicomp.Editor
	keybindings    *tui.KeybindingsManager
	actionHandlers map[tui.AppAction]func()

	// Dynamic handlers that can be replaced
	OnEscape            func()
	OnCtrlD             func()
	OnPasteImage        func()
	OnExtensionShortcut func(data string) bool
}

// NewCustomEditor creates a new CustomEditor.
func NewCustomEditor(t *tui.TUI, theme tuicomp.EditorTheme, keybindings *tui.KeybindingsManager, opts ...tuicomp.EditorOptions) *CustomEditor {
	return &CustomEditor{
		Editor:         tuicomp.NewEditor(t, theme, opts...),
		keybindings:    keybindings,
		actionHandlers: make(map[tui.AppAction]func()),
	}
}

// OnAction registers a handler for an app action.
func (ce *CustomEditor) OnAction(action tui.AppAction, handler func()) {
	ce.actionHandlers[action] = handler
}

// HandleInput processes keyboard input, checking app keybindings first.
func (ce *CustomEditor) HandleInput(data string) {
	// Check extension shortcuts first
	if ce.OnExtensionShortcut != nil && ce.OnExtensionShortcut(data) {
		return
	}

	// Check paste image
	if ce.keybindings.Matches(data, tui.ActionPasteImage) {
		if ce.OnPasteImage != nil {
			ce.OnPasteImage()
		}
		return
	}

	// Escape/interrupt — only if autocomplete is NOT active
	if ce.keybindings.Matches(data, tui.ActionInterrupt) {
		if !ce.IsShowingAutocomplete() {
			handler := ce.OnEscape
			if handler == nil {
				handler = ce.actionHandlers[tui.ActionInterrupt]
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
	if ce.keybindings.Matches(data, tui.ActionExit) {
		if ce.GetText() == "" {
			handler := ce.OnCtrlD
			if handler == nil {
				handler = ce.actionHandlers[tui.ActionExit]
			}
			if handler != nil {
				handler()
			}
			return
		}
		// Fall through to editor handling for delete-char-forward when not empty
	}

	// \x1b\r is ambiguous in legacy (non-Kitty) terminals: it means "alt+enter"
	// in MatchesKey, but legacy terminals that don't support modifyOtherKeys or
	// the Kitty protocol also send it for shift+enter. Prefer shift+enter
	// (newline insertion) over alt+enter (follow-up) for this sequence because:
	//   1. fir always enables modifyOtherKeys (\x1b[>4;2m); terminals that
	//      support it will send \x1b[27;3;13~ for alt+enter, so \x1b\r only
	//      arrives from truly legacy terminals where shift+enter is expected.
	//   2. The editor's built-in newline handler recognises \x1b\r explicitly.
	// In Kitty mode \x1b\r is unambiguously shift+enter and is handled
	// correctly already (alt+enter won't match it there).
	if data == "\x1b\r" {
		ce.Editor.HandleInput(data)
		return
	}

	// Check all other app actions
	for action, handler := range ce.actionHandlers {
		if action != tui.ActionInterrupt && action != tui.ActionExit && ce.keybindings.Matches(data, action) {
			handler()
			return
		}
	}

	// Pass to parent for editor handling
	ce.Editor.HandleInput(data)
}
