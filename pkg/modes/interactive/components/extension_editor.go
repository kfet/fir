// Ported from: packages/coding-agent/src/modes/interactive/components/extension-editor.ts
// Upstream hash: 1caadb2e
package components

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ExtensionEditorComponent is a multi-line editor component for extensions.
type ExtensionEditorComponent struct {
	tui.Container
	editor      *tuicomp.Editor
	onSubmit    func(value string)
	onCancel    func()
	tuiRef      *tui.TUI
	keybindings *core.KeybindingsManager
}

// NewExtensionEditorComponent creates a new ExtensionEditorComponent.
func NewExtensionEditorComponent(
	tuiRef *tui.TUI,
	keybindings *core.KeybindingsManager,
	title string,
	prefill string,
	onSubmit func(value string),
	onCancel func(),
	opts ...tuicomp.EditorOptions,
) *ExtensionEditorComponent {
	t := theme.GetTheme()
	c := &ExtensionEditorComponent{
		onSubmit:    onSubmit,
		onCancel:    onCancel,
		tuiRef:      tuiRef,
		keybindings: keybindings,
	}

	// Top border
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	// Title
	c.AddChild(tuicomp.NewText(t.Fg("accent", title), 1, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	// Create editor
	c.editor = tuicomp.NewEditor(tuiRef, theme.GetEditorTheme(), opts...)
	if prefill != "" {
		c.editor.SetText(prefill)
	}
	c.editor.OnSubmit = func(text string) {
		c.onSubmit(text)
	}
	c.AddChild(c.editor)

	c.AddChild(tuicomp.NewSpacer(1))

	// Hint
	hasExternalEditor := os.Getenv("VISUAL") != "" || os.Getenv("EDITOR") != ""
	hint := KeyHint(tuicomp.ActSelectConfirm, "submit") +
		"  " + KeyHint(tuicomp.ActNewLine, "newline") +
		"  " + KeyHint(tuicomp.ActSelectCancel, "cancel")
	if hasExternalEditor {
		hint += "  " + AppKeyHint(keybindings, "externalEditor", "external editor")
	}
	c.AddChild(tuicomp.NewText(hint, 1, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	// Bottom border
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// HandleInput processes keyboard input.
func (c *ExtensionEditorComponent) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		c.onCancel()
		return
	}

	if c.keybindings.Matches(data, "externalEditor") {
		c.openExternalEditor()
		return
	}

	c.editor.HandleInput(data)
}

func (c *ExtensionEditorComponent) openExternalEditor() {
	editorCmd := os.Getenv("VISUAL")
	if editorCmd == "" {
		editorCmd = os.Getenv("EDITOR")
	}
	if editorCmd == "" {
		return
	}

	currentText := c.editor.GetText()
	tmpFile := filepath.Join(os.TempDir(), "fir-extension-editor-"+strconv.FormatInt(time.Now().UnixMilli(), 10)+".md")

	if err := os.WriteFile(tmpFile, []byte(currentText), 0o644); err != nil {
		return
	}
	defer os.Remove(tmpFile)

	if c.tuiRef != nil {
		c.tuiRef.Stop()
	}

	parts := strings.Fields(editorCmd)
	args := append(parts[1:], tmpFile)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err == nil {
		if newContent, err := os.ReadFile(tmpFile); err == nil {
			text := strings.TrimSuffix(string(newContent), "\n")
			c.editor.SetText(text)
		}
	}

	if c.tuiRef != nil {
		c.tuiRef.Start()
		c.tuiRef.RequestRender(true)
	}
}
