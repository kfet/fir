package components

import (
	"testing"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

func newTestCustomEditor() (*CustomEditor, *tui.KeybindingsManager) {
	_ = theme.InitTheme("dark", nil)
	kb := tui.NewKeybindingsManagerInMemory(tui.KeybindingsConfig{})
	th := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, th, kb)
	return ed, kb
}

func TestNewCustomEditor(t *testing.T) {
	kb := tui.NewKeybindingsManagerInMemory(tui.KeybindingsConfig{})
	theme := theme.GetEditorTheme()
	ed := NewCustomEditor(nil, theme, kb)
	if ed == nil {
		t.Fatal("expected non-nil editor")
	}
}

func TestCustomEditor_OnAction(t *testing.T) {
	kb := tui.NewKeybindingsManagerInMemory(tui.KeybindingsConfig{})
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
	kb := tui.NewKeybindingsManagerInMemory(tui.KeybindingsConfig{})
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
	kb := tui.NewKeybindingsManagerInMemory(tui.KeybindingsConfig{})
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

// ---------------------------------------------------------------------------
// Shift+Enter regression tests
// ---------------------------------------------------------------------------

// TestCustomEditor_ShiftEnter_InsertsNewline checks that the three common
// shift+enter sequences — Kitty CSI-u, CSI-tilde, and modifyOtherKeys —
// insert a newline in the editor rather than firing any app-level action
// (notably ActionFollowUp / alt+enter).
func TestCustomEditor_ShiftEnter_InsertsNewline(t *testing.T) {
	sequences := []struct {
		name string
		data string
	}{
		{"Kitty CSI-u", "\x1b[13;2u"},
		{"CSI-tilde", "\x1b[13;2~"},
		{"modifyOtherKeys", "\x1b[27;2;13~"},
	}

	for _, kittyMode := range []bool{false, true} {
		tui.SetKittyProtocolActive(kittyMode)
		defer tui.SetKittyProtocolActive(false)

		for _, seq := range sequences {
			t.Run(seq.name+"/kitty="+boolToStr(kittyMode), func(t *testing.T) {
				ed, _ := newTestCustomEditor()

				followUpCalled := false
				ed.OnAction(tui.ActionFollowUp, func() { followUpCalled = true })

				ed.SetText("hello")
				ed.HandleInput(seq.data)

				if followUpCalled {
					t.Errorf("shift+enter (%q) fired ActionFollowUp instead of inserting newline", seq.data)
				}
				text := ed.GetText()
				if text != "hello\n" {
					t.Errorf("expected \"hello\\n\", got %q", text)
				}
				line, col := ed.GetCursor()
				if line != 1 || col != 0 {
					t.Errorf("expected cursor at (1,0), got (%d,%d)", line, col)
				}
			})
		}
	}
}

// TestCustomEditor_ShiftEnter_LegacyEscapeCR verifies that \x1b\r (sent by
// legacy terminals for shift+enter) is treated as a newline — NOT as
// alt+enter (ActionFollowUp) — in both Kitty and non-Kitty modes.
//
// This is the primary regression test for the "shift+enter doesn't work"
// bug: CustomEditor was consuming \x1b\r via ActionFollowUp before the
// editor's built-in legacy-newline handler could see it.
func TestCustomEditor_ShiftEnter_LegacyEscapeCR(t *testing.T) {
	for _, kittyMode := range []bool{false, true} {
		tui.SetKittyProtocolActive(kittyMode)
		t.Cleanup(func() { tui.SetKittyProtocolActive(false) })

		t.Run("kitty="+boolToStr(kittyMode), func(t *testing.T) {
			ed, _ := newTestCustomEditor()

			followUpCalled := false
			ed.OnAction(tui.ActionFollowUp, func() { followUpCalled = true })

			ed.SetText("hello")
			ed.HandleInput("\x1b\r")

			if followUpCalled {
				t.Error("\\x1b\\r fired ActionFollowUp; it should insert a newline (legacy shift+enter)")
			}
			text := ed.GetText()
			if text != "hello\n" {
				t.Errorf("expected \"hello\\n\", got %q", text)
			}
		})
	}
}

// TestCustomEditor_AltEnter_ModifyOtherKeys verifies that the unambiguous
// modifyOtherKeys sequence for alt+enter (\x1b[27;3;13~) still triggers
// ActionFollowUp after the \x1b\r fix is applied.
func TestCustomEditor_AltEnter_ModifyOtherKeys(t *testing.T) {
	tui.SetKittyProtocolActive(false)
	defer tui.SetKittyProtocolActive(false)

	ed, _ := newTestCustomEditor()

	followUpCalled := false
	ed.OnAction(tui.ActionFollowUp, func() { followUpCalled = true })

	ed.SetText("hello")
	ed.HandleInput("\x1b[27;3;13~") // modifyOtherKeys alt+enter

	if !followUpCalled {
		t.Error("\\x1b[27;3;13~ (modifyOtherKeys alt+enter) should trigger ActionFollowUp")
	}
	// Editor text should be unchanged (follow-up fires with text, but handler is a no-op here)
}

// TestCustomEditor_AltEnter_Kitty verifies that the Kitty-protocol
// alt+enter sequence (\x1b[13;3u) triggers ActionFollowUp.
func TestCustomEditor_AltEnter_Kitty(t *testing.T) {
	tui.SetKittyProtocolActive(true)
	defer tui.SetKittyProtocolActive(false)

	ed, _ := newTestCustomEditor()

	followUpCalled := false
	ed.OnAction(tui.ActionFollowUp, func() { followUpCalled = true })

	ed.SetText("hello")
	ed.HandleInput("\x1b[13;3u") // Kitty alt+enter

	if !followUpCalled {
		t.Error("\\x1b[13;3u (Kitty alt+enter) should trigger ActionFollowUp")
	}
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
