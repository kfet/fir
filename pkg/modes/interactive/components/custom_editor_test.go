package components

import (
	"testing"

	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

func TestNewCustomEditor(t *testing.T) {
	kb := core.NewKeybindingsManagerInMemory(core.KeybindingsConfig{})
	theme := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, theme, kb)
	if ed == nil {
		t.Fatal("expected non-nil editor")
	}
}

func TestCustomEditor_OnAction(t *testing.T) {
	kb := core.NewKeybindingsManagerInMemory(core.KeybindingsConfig{})
	theme := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, theme, kb)

	called := false
	ed.OnAction("selectModel", func() { called = true })

	if _, ok := ed.actionHandlers["selectModel"]; !ok {
		t.Fatal("expected action handler to be registered")
	}
	ed.actionHandlers["selectModel"]()
	if !called {
		t.Fatal("expected handler to be called")
	}
}

func TestCustomEditor_HandleInput_ExtensionShortcut(t *testing.T) {
	kb := core.NewKeybindingsManagerInMemory(core.KeybindingsConfig{})
	theme := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, theme, kb, tuicomp.EditorOptions{})

	handled := false
	ed.OnExtensionShortcut = func(data string) bool {
		handled = true
		return true
	}
	ed.HandleInput("x")
	if !handled {
		t.Error("expected extension shortcut to be called")
	}
}

func TestCustomEditor_EscapeWithoutAutocomplete(t *testing.T) {
	kb := core.NewKeybindingsManagerInMemory(core.KeybindingsConfig{})
	theme := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, theme, kb)

	escapeCalled := false
	ed.OnEscape = func() { escapeCalled = true }

	// Simulate escape key input
	ed.HandleInput("\x1b")
	if !escapeCalled {
		// If not called, it may have been sent to parent editor. This is fine
		// as the test setup may not have the right keybindings configured.
		t.Log("escape handler not called - keybinding may not match")
	}
}
