package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/tui"
)

// ---------------------------------------------------------------------------
// Raw terminal escape sequences for HandleInput
// ---------------------------------------------------------------------------

const (
	keyUp                 = "\x1b[A"
	keyDown               = "\x1b[B"
	keyRight              = "\x1b[C"
	keyLeft               = "\x1b[D"
	keyHome               = "\x1b[H"
	keyEnd                = "\x1b[F"
	keyBackspace          = "\x7f"
	keyDelete             = "\x1b[3~"
	keyEnter              = "\r"
	keyEscape             = "\x1b"
	keyAltLeft            = "\x1b[1;3D"
	keyAltRight           = "\x1b[1;3C"
	keyCtrlK              = "\x0b"
	keyCtrlU              = "\x15"
	keyCtrlW              = "\x17"
	keyCtrlY              = "\x19"
	keyCtrlMinus          = "\x1f"
	keyCtrlRBrk           = "\x1d"
	keyShiftEnter         = "\x1b[13;2~"
	keyShiftEnterModOther = "\x1b[27;2;13~"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestEditor() *Editor {
	theme := EditorTheme{
		BorderColor: func(s string) string { return s },
		SelectList: SelectListTheme{
			SelectedPrefix: func(s string) string { return s },
			SelectedText:   func(s string) string { return s },
			Description:    func(s string) string { return s },
			ScrollInfo:     func(s string) string { return s },
			NoMatch:        func(s string) string { return s },
		},
	}
	return NewEditor(nil, theme)
}

func attachOnChange(e *Editor) *string {
	var last string
	e.OnChange = func(text string) { last = text }
	return &last
}

// ---------------------------------------------------------------------------
// WordWrapLine tests
// ---------------------------------------------------------------------------

func TestWordWrapLine_Empty(t *testing.T) {
	chunks := WordWrapLine("", 10)
	if len(chunks) != 1 || chunks[0].Text != "" {
		t.Fatalf("expected one empty chunk, got %v", chunks)
	}
}

func TestWordWrapLine_FitsInWidth(t *testing.T) {
	chunks := WordWrapLine("hello world", 80)
	if len(chunks) != 1 || chunks[0].Text != "hello world" {
		t.Fatalf("expected one chunk, got %v", chunks)
	}
}

func TestWordWrapLine_WrapsAtWordBoundary(t *testing.T) {
	chunks := WordWrapLine("hello world", 6)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0].Text, "hello") {
		t.Errorf("first chunk should start with 'hello', got %q", chunks[0].Text)
	}
}

func TestWordWrapLine_LongWordForcedBreak(t *testing.T) {
	chunks := WordWrapLine("abcdefghij", 3)
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks for 10-char word in width 3, got %d", len(chunks))
	}
}

func TestWordWrapLine_ZeroWidth(t *testing.T) {
	chunks := WordWrapLine("hello", 0)
	if len(chunks) != 1 || chunks[0].Text != "" {
		t.Fatalf("expected empty chunk for zero width, got %v", chunks)
	}
}

func TestWordWrapLine_ExactWidth(t *testing.T) {
	chunks := WordWrapLine("hello", 5)
	if len(chunks) != 1 || chunks[0].Text != "hello" {
		t.Fatalf("expected one chunk 'hello', got %v", chunks)
	}
}

func TestWordWrapLine_ChunkIndices(t *testing.T) {
	chunks := WordWrapLine("hello world", 6)
	for _, c := range chunks {
		if c.EndIndex > len("hello world") {
			t.Errorf("chunk EndIndex %d exceeds line length", c.EndIndex)
		}
		if c.StartIndex > c.EndIndex {
			t.Errorf("StartIndex %d > EndIndex %d", c.StartIndex, c.EndIndex)
		}
	}
}

// ---------------------------------------------------------------------------
// Editor creation and basic state
// ---------------------------------------------------------------------------

func TestEditor_NewEditorDefaults(t *testing.T) {
	e := newTestEditor()
	if e.GetText() != "" {
		t.Errorf("new editor text should be empty, got %q", e.GetText())
	}
	line, col := e.GetCursor()
	if line != 0 || col != 0 {
		t.Errorf("cursor should be at (0,0), got (%d,%d)", line, col)
	}
	if e.Focused {
		t.Error("editor should not be focused by default")
	}
}

func TestEditor_SetText(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello\nworld")
	if e.GetText() != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got %q", e.GetText())
	}
	line, col := e.GetCursor()
	if line != 1 || col != 5 {
		t.Errorf("cursor should be at end (1,5), got (%d,%d)", line, col)
	}
}

func TestEditor_SetText_CRLF(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello\r\nworld")
	if e.GetText() != "hello\nworld" {
		t.Errorf("CRLF should be normalized, got %q", e.GetText())
	}
}

func TestEditor_GetLines(t *testing.T) {
	e := newTestEditor()
	e.SetText("abc\ndef")
	lines := e.GetLines()
	if len(lines) != 2 || lines[0] != "abc" || lines[1] != "def" {
		t.Errorf("unexpected lines: %v", lines)
	}
	lines[0] = "xxx"
	if e.GetLines()[0] != "abc" {
		t.Error("GetLines should return a copy")
	}
}

// ---------------------------------------------------------------------------
// Character insertion
// ---------------------------------------------------------------------------

func TestEditor_InsertCharacter(t *testing.T) {
	e := newTestEditor()
	changed := attachOnChange(e)

	e.HandleInput("h")
	e.HandleInput("i")

	if e.GetText() != "hi" {
		t.Errorf("expected 'hi', got %q", e.GetText())
	}
	if *changed != "hi" {
		t.Errorf("onChange should have 'hi', got %q", *changed)
	}
	_, col := e.GetCursor()
	if col != 2 {
		t.Errorf("cursor col should be 2, got %d", col)
	}
}

func TestEditor_InsertTextAtCursor(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello")
	e.InsertTextAtCursor(" world")
	if e.GetText() != "hello world" {
		t.Errorf("expected 'hello world', got %q", e.GetText())
	}
}

func TestEditor_InsertMultilineAtCursor(t *testing.T) {
	e := newTestEditor()
	e.SetText("ac")
	e.HandleInput(keyLeft) // cursor at col 1
	e.InsertTextAtCursor("b\nd")

	text := e.GetText()
	if text != "ab\ndc" {
		t.Errorf("expected 'ab\\ndc', got %q", text)
	}
}

// ---------------------------------------------------------------------------
// Backspace / delete
// ---------------------------------------------------------------------------

func TestEditor_Backspace(t *testing.T) {
	e := newTestEditor()
	e.SetText("ab")
	e.HandleInput(keyBackspace)
	if e.GetText() != "a" {
		t.Errorf("expected 'a', got %q", e.GetText())
	}
}

func TestEditor_Backspace_AtStart_MergesLines(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello\nworld")
	e.HandleInput(keyHome)
	e.HandleInput(keyBackspace)
	if e.GetText() != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", e.GetText())
	}
}

func TestEditor_ForwardDelete(t *testing.T) {
	e := newTestEditor()
	e.SetText("ab")
	e.HandleInput(keyHome) // cursor at 0
	e.HandleInput(keyDelete)
	if e.GetText() != "b" {
		t.Errorf("expected 'b', got %q", e.GetText())
	}
}

func TestEditor_ForwardDelete_AtEnd_MergesLines(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello\nworld")
	e.HandleInput(keyUp)
	e.HandleInput(keyEnd)
	e.HandleInput(keyDelete)
	if e.GetText() != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// New line
// ---------------------------------------------------------------------------

func TestEditor_NewLine(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello")
	e.HandleInput(keyShiftEnter)
	if e.GetText() != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", e.GetText())
	}
	line, col := e.GetCursor()
	if line != 1 || col != 0 {
		t.Errorf("cursor should be at (1,0), got (%d,%d)", line, col)
	}
}

// TestEditor_NewLine_ModifyOtherKeys verifies that the modifyOtherKeys level-2
// Shift+Enter sequence (\x1b[27;2;13~) correctly inserts a newline.  This is
// the sequence emitted by xterm/iTerm2 when the terminal is put into
// modifyOtherKeys mode (which fir now enables on startup).
func TestEditor_NewLine_ModifyOtherKeys(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello")
	e.HandleInput(keyShiftEnterModOther)
	if e.GetText() != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", e.GetText())
	}
	line, col := e.GetCursor()
	if line != 1 || col != 0 {
		t.Errorf("cursor should be at (1,0), got (%d,%d)", line, col)
	}
}

// ---------------------------------------------------------------------------
// Submit
// ---------------------------------------------------------------------------

func TestEditor_Submit(t *testing.T) {
	e := newTestEditor()
	var submitted string
	e.OnSubmit = func(text string) { submitted = text }

	e.HandleInput("h")
	e.HandleInput("i")
	e.HandleInput(keyEnter)

	if submitted != "hi" {
		t.Errorf("expected submitted 'hi', got %q", submitted)
	}
	if e.GetText() != "" {
		t.Errorf("editor should be empty after submit, got %q", e.GetText())
	}
}

func TestEditor_Submit_DisableSubmit(t *testing.T) {
	e := newTestEditor()
	e.DisableSubmit = true
	var submitted bool
	e.OnSubmit = func(text string) { submitted = true }

	e.HandleInput("h")
	e.HandleInput("i")
	e.HandleInput(keyEnter)

	if submitted {
		t.Error("submit should be disabled")
	}
}

func TestEditor_Submit_TrimsWhitespace(t *testing.T) {
	e := newTestEditor()
	var submitted string
	e.OnSubmit = func(text string) { submitted = text }

	e.HandleInput(" ")
	e.HandleInput("h")
	e.HandleInput("i")
	e.HandleInput(" ")
	e.HandleInput(keyEnter)

	if submitted != "hi" {
		t.Errorf("expected trimmed 'hi', got %q", submitted)
	}
}

// ---------------------------------------------------------------------------
// Cursor movement
// ---------------------------------------------------------------------------

func TestEditor_CursorLeftRight(t *testing.T) {
	e := newTestEditor()
	e.SetText("abc")

	e.HandleInput(keyHome)
	_, col := e.GetCursor()
	if col != 0 {
		t.Errorf("expected col 0 after Home, got %d", col)
	}

	e.HandleInput(keyRight)
	_, col = e.GetCursor()
	if col != 1 {
		t.Errorf("expected col 1 after Right, got %d", col)
	}

	e.HandleInput(keyEnd)
	_, col = e.GetCursor()
	if col != 3 {
		t.Errorf("expected col 3 after End, got %d", col)
	}

	e.HandleInput(keyLeft)
	_, col = e.GetCursor()
	if col != 2 {
		t.Errorf("expected col 2 after Left, got %d", col)
	}
}

func TestEditor_CursorUpDown(t *testing.T) {
	e := newTestEditor()
	e.SetText("first\nsecond")

	line, _ := e.GetCursor()
	if line != 1 {
		t.Fatalf("expected cursor at line 1, got %d", line)
	}

	e.HandleInput(keyUp)
	line, _ = e.GetCursor()
	if line != 0 {
		t.Errorf("expected line 0 after Up, got %d", line)
	}

	e.HandleInput(keyDown)
	line, _ = e.GetCursor()
	if line != 1 {
		t.Errorf("expected line 1 after Down, got %d", line)
	}
}

func TestEditor_CursorRight_WrapsToNextLine(t *testing.T) {
	e := newTestEditor()
	e.SetText("ab\ncd")
	e.HandleInput(keyUp)
	e.HandleInput(keyEnd)
	e.HandleInput(keyRight)
	line, col := e.GetCursor()
	if line != 1 || col != 0 {
		t.Errorf("expected (1,0), got (%d,%d)", line, col)
	}
}

func TestEditor_CursorLeft_WrapsToPreviousLine(t *testing.T) {
	e := newTestEditor()
	e.SetText("ab\ncd")
	e.HandleInput(keyUp)
	e.HandleInput(keyDown)
	e.HandleInput(keyHome)
	e.HandleInput(keyLeft)
	line, col := e.GetCursor()
	if line != 0 || col != 2 {
		t.Errorf("expected (0,2), got (%d,%d)", line, col)
	}
}

// ---------------------------------------------------------------------------
// Word movement
// ---------------------------------------------------------------------------

func TestEditor_MoveWordForward(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world foo")
	e.HandleInput(keyHome)
	e.HandleInput(keyAltRight)
	_, col := e.GetCursor()
	if col != 5 {
		t.Errorf("expected col 5 after word forward, got %d", col)
	}
}

func TestEditor_MoveWordBackward(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	e.HandleInput(keyAltLeft)
	_, col := e.GetCursor()
	if col != 6 {
		t.Errorf("expected col 6 after word backward, got %d", col)
	}
}

// ---------------------------------------------------------------------------
// Delete to line start/end (kill)
// ---------------------------------------------------------------------------

func TestEditor_DeleteToLineEnd(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	e.HandleInput(keyHome)
	e.HandleInput(keyCtrlK)
	if e.GetText() != "" {
		t.Errorf("expected empty after Ctrl+K from start, got %q", e.GetText())
	}
}

func TestEditor_DeleteToLineStart(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	e.HandleInput(keyCtrlU)
	if e.GetText() != "" {
		t.Errorf("expected empty after Ctrl+U from end, got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Word delete
// ---------------------------------------------------------------------------

func TestEditor_DeleteWordBackward(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	e.HandleInput(keyCtrlW)
	if e.GetText() != "hello " {
		t.Errorf("expected 'hello ' after Ctrl+W, got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Kill ring / Yank
// ---------------------------------------------------------------------------

func TestEditor_KillAndYank(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello world")
	e.HandleInput(keyHome)
	e.HandleInput(keyCtrlK) // kill "hello world"
	if e.GetText() != "" {
		t.Errorf("expected empty, got %q", e.GetText())
	}

	e.HandleInput(keyCtrlY) // yank
	if e.GetText() != "hello world" {
		t.Errorf("expected 'hello world' after yank, got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Undo
// ---------------------------------------------------------------------------

func TestEditor_Undo(t *testing.T) {
	e := newTestEditor()
	e.SetText("abc")
	e.HandleInput(" ") // triggers undo snapshot

	e.HandleInput(keyCtrlMinus)
	text := e.GetText()
	if text != "abc" {
		t.Errorf("expected 'abc' after undo, got %q", text)
	}
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func TestEditor_History(t *testing.T) {
	e := newTestEditor()
	e.AddToHistory("first prompt")
	e.AddToHistory("second prompt")

	e.HandleInput(keyUp)
	if e.GetText() != "second prompt" {
		t.Errorf("expected 'second prompt', got %q", e.GetText())
	}

	e.HandleInput(keyUp)
	if e.GetText() != "first prompt" {
		t.Errorf("expected 'first prompt', got %q", e.GetText())
	}

	e.HandleInput(keyDown)
	if e.GetText() != "second prompt" {
		t.Errorf("expected 'second prompt', got %q", e.GetText())
	}

	e.HandleInput(keyDown)
	if e.GetText() != "" {
		t.Errorf("expected empty on return from history, got %q", e.GetText())
	}
}

func TestEditor_History_NoDuplicates(t *testing.T) {
	e := newTestEditor()
	e.AddToHistory("same")
	e.AddToHistory("same")

	e.HandleInput(keyUp)
	if e.GetText() != "same" {
		t.Errorf("expected 'same', got %q", e.GetText())
	}
	e.HandleInput(keyUp)
	if e.GetText() != "same" {
		t.Errorf("still expected 'same', got %q", e.GetText())
	}
}

func TestEditor_History_EmptyIgnored(t *testing.T) {
	e := newTestEditor()
	e.AddToHistory("   ")
	e.AddToHistory("")
	e.HandleInput(keyUp)
	if e.GetText() != "" {
		t.Errorf("expected empty, got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Render tests
// ---------------------------------------------------------------------------

func TestEditor_BorderSingleEscape(t *testing.T) {
	// Regression: border was repeating a per-character colored "─" string,
	// producing hundreds of redundant ANSI escapes that corrupted tmux output.
	e := newTestEditor()
	lines := e.Render(80)
	top := lines[0]
	bottom := lines[len(lines)-1]
	// Each border should contain at most 2 SGR escape sequences (open + close),
	// not one pair per dash character.
	for _, border := range []string{top, bottom} {
		count := strings.Count(border, "\x1b[")
		if count > 4 {
			t.Errorf("border has %d escape sequences (expected ≤4), line: %q", count, border)
		}
	}
}

func TestEditor_RenderEmpty(t *testing.T) {
	e := newTestEditor()
	lines := e.Render(40)

	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "─") {
		t.Errorf("expected top border, got %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "─") {
		t.Errorf("expected bottom border, got %q", lines[len(lines)-1])
	}
}

func TestEditor_RenderWithText(t *testing.T) {
	e := newTestEditor()
	e.SetText("hello")
	lines := e.Render(40)

	found := false
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rendered output should contain 'hello', got %v", lines)
	}
}

func TestEditor_RenderCursorFocused(t *testing.T) {
	e := newTestEditor()
	e.SetText("hi")
	e.Focused = true
	lines := e.Render(40)

	found := false
	for _, line := range lines {
		if strings.Contains(line, tui.CursorMarker) {
			found = true
			break
		}
	}
	if !found {
		t.Error("focused editor should emit CursorMarker")
	}
}

func TestEditor_RenderCursorNotFocused(t *testing.T) {
	e := newTestEditor()
	e.SetText("hi")
	e.Focused = false
	lines := e.Render(40)

	for _, line := range lines {
		if strings.Contains(line, tui.CursorMarker) {
			t.Error("unfocused editor should not emit CursorMarker")
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Scroll
// ---------------------------------------------------------------------------

func TestEditor_ScrollIndicators(t *testing.T) {
	e := newTestEditor()
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("line")
	}
	e.SetText(sb.String())

	lines := e.Render(40)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 rendered lines, got %d", len(lines))
	}
}

// ---------------------------------------------------------------------------
// Paste handling
// ---------------------------------------------------------------------------

func TestEditor_BracketedPaste(t *testing.T) {
	e := newTestEditor()
	e.HandleInput("\x1b[200~hello\x1b[201~")
	if e.GetText() != "hello" {
		t.Errorf("expected 'hello' from paste, got %q", e.GetText())
	}
}

func TestEditor_LargePaste_CreatesMarker(t *testing.T) {
	e := newTestEditor()
	longText := strings.Repeat("x", 1100)
	e.HandleInput("\x1b[200~" + longText + "\x1b[201~")

	text := e.GetText()
	if !strings.Contains(text, "[paste #") {
		t.Errorf("large paste should create marker, got %q", text)
	}

	expanded := e.GetExpandedText()
	if !strings.Contains(expanded, longText) {
		t.Error("expanded text should contain the pasted content")
	}
}

func TestEditor_MultilinePaste_CreatesMarker(t *testing.T) {
	e := newTestEditor()
	var lineStrs []string
	for i := 0; i < 15; i++ {
		lineStrs = append(lineStrs, "line")
	}
	multiline := strings.Join(lineStrs, "\n")
	e.HandleInput("\x1b[200~" + multiline + "\x1b[201~")

	text := e.GetText()
	if !strings.Contains(text, "[paste #") {
		t.Errorf("large multiline paste should create marker, got %q", text)
	}
}

func TestEditor_SmallMultilinePaste(t *testing.T) {
	e := newTestEditor()
	e.HandleInput("\x1b[200~hello\nworld\x1b[201~")
	if e.GetText() != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got %q", e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Jump to character
// ---------------------------------------------------------------------------

func TestEditor_JumpForward(t *testing.T) {
	e := newTestEditor()
	e.SetText("abcdefg")
	e.HandleInput(keyHome)
	e.HandleInput(keyCtrlRBrk) // Ctrl+] = jump forward mode
	e.HandleInput("d")

	_, col := e.GetCursor()
	if col != 3 {
		t.Errorf("expected cursor at col 3 (on 'd'), got %d", col)
	}
}

// ---------------------------------------------------------------------------
// Autocomplete
// ---------------------------------------------------------------------------

type mockAutocomplete struct {
	suggestions *AutocompleteSuggestions
	applied     bool
}

func (m *mockAutocomplete) GetSuggestions(lines []string, cursorLine, cursorCol int) *AutocompleteSuggestions {
	return m.suggestions
}

func (m *mockAutocomplete) ApplyCompletion(lines []string, cursorLine, cursorCol int, item SelectItem, prefix string) ApplyCompletionResult {
	m.applied = true
	newLines := make([]string, len(lines))
	copy(newLines, lines)
	line := newLines[cursorLine]
	newLines[cursorLine] = line[:cursorCol-len(prefix)] + item.Value
	newCol := cursorCol - len(prefix) + len(item.Value)
	return ApplyCompletionResult{
		Lines:      newLines,
		CursorLine: cursorLine,
		CursorCol:  newCol,
	}
}

func TestEditor_AutocompleteSlashCommand(t *testing.T) {
	e := newTestEditor()
	ac := &mockAutocomplete{
		suggestions: &AutocompleteSuggestions{
			Items: []SelectItem{
				{Value: "/help", Label: "/help", Description: "Show help"},
			},
			Prefix: "/",
		},
	}
	e.SetAutocompleteProvider(ac)

	e.HandleInput("/")
	if !e.IsShowingAutocomplete() {
		t.Error("autocomplete should be showing after typing /")
	}
}

func TestEditor_AutocompleteCancel(t *testing.T) {
	e := newTestEditor()
	ac := &mockAutocomplete{
		suggestions: &AutocompleteSuggestions{
			Items:  []SelectItem{{Value: "/help"}},
			Prefix: "/",
		},
	}
	e.SetAutocompleteProvider(ac)

	e.HandleInput("/")
	if !e.IsShowingAutocomplete() {
		t.Fatal("autocomplete should be showing")
	}

	e.HandleInput(keyEscape)
	if e.IsShowingAutocomplete() {
		t.Error("autocomplete should be cancelled after Escape")
	}
}

func TestEditor_AutocompleteRendered(t *testing.T) {
	e := newTestEditor()
	ac := &mockAutocomplete{
		suggestions: &AutocompleteSuggestions{
			Items:  []SelectItem{{Value: "/help", Label: "/help"}},
			Prefix: "/",
		},
	}
	e.SetAutocompleteProvider(ac)

	e.HandleInput("/")
	lines := e.Render(40)

	found := false
	for _, line := range lines {
		if strings.Contains(line, "/help") {
			found = true
			break
		}
	}
	if !found {
		t.Error("autocomplete items should be rendered")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEditor_BackspaceOnEmptyDoesNotPanic(t *testing.T) {
	e := newTestEditor()
	e.HandleInput(keyBackspace)
	if e.GetText() != "" {
		t.Error("expected empty")
	}
}

func TestEditor_DeleteOnEmptyDoesNotPanic(t *testing.T) {
	e := newTestEditor()
	e.HandleInput(keyDelete)
	if e.GetText() != "" {
		t.Error("expected empty")
	}
}

func TestEditor_CursorBoundsRespected(t *testing.T) {
	e := newTestEditor()
	e.HandleInput(keyLeft)
	_, col := e.GetCursor()
	if col != 0 {
		t.Errorf("col should be 0 on empty, got %d", col)
	}

	e.HandleInput(keyRight)
	_, col = e.GetCursor()
	if col != 0 {
		t.Errorf("col should still be 0, got %d", col)
	}
}

func TestEditor_UndoOnEmptyDoesNotPanic(t *testing.T) {
	e := newTestEditor()
	e.HandleInput(keyCtrlMinus)
	if e.GetText() != "" {
		t.Error("expected empty")
	}
}

// ---------------------------------------------------------------------------
// Kitty CSI-u printable decoding
// ---------------------------------------------------------------------------

func TestDecodeKittyPrintable_Simple(t *testing.T) {
	ch, ok := decodeKittyPrintable("\x1b[97u")
	if !ok || ch != "a" {
		t.Errorf("expected 'a', got %q ok=%v", ch, ok)
	}
}

func TestDecodeKittyPrintable_ShiftedKey(t *testing.T) {
	ch, ok := decodeKittyPrintable("\x1b[97:65;2u")
	if !ok || ch != "A" {
		t.Errorf("expected 'A', got %q ok=%v", ch, ok)
	}
}

func TestDecodeKittyPrintable_CtrlIgnored(t *testing.T) {
	_, ok := decodeKittyPrintable("\x1b[97;5u")
	if ok {
		t.Error("Ctrl sequences should be ignored")
	}
}

func TestDecodeKittyPrintable_AltIgnored(t *testing.T) {
	_, ok := decodeKittyPrintable("\x1b[97;3u")
	if ok {
		t.Error("Alt sequences should be ignored")
	}
}

func TestDecodeKittyPrintable_ControlCharRejected(t *testing.T) {
	_, ok := decodeKittyPrintable("\x1b[10u")
	if ok {
		t.Error("control characters should be rejected")
	}
}

func TestDecodeKittyPrintable_NoMatch(t *testing.T) {
	_, ok := decodeKittyPrintable("hello")
	if ok {
		t.Error("non-CSI-u should not match")
	}
}

// ---------------------------------------------------------------------------
// Render doesn't crash with various states
// ---------------------------------------------------------------------------

func TestEditor_RenderVariousStates(t *testing.T) {
	e := newTestEditor()
	widths := []int{1, 5, 10, 40, 80, 120}

	for _, w := range widths {
		e.Render(w)
	}

	e.SetText("hello world this is a longer text line")
	for _, w := range widths {
		e.Render(w)
	}

	e.SetText("line one\nline two\nline three\nfour\nfive\nsix")
	for _, w := range widths {
		e.Render(w)
	}
}

// ---------------------------------------------------------------------------
// SetFocused
// ---------------------------------------------------------------------------

func TestEditor_SetFocused(t *testing.T) {
	e := newTestEditor()
	e.SetFocused(true)
	if !e.Focused {
		t.Error("expected focused to be true")
	}
	e.SetFocused(false)
	if e.Focused {
		t.Error("expected focused to be false")
	}
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Combined multi-operation scenarios
// ---------------------------------------------------------------------------

func TestEditor_TypeAndDelete(t *testing.T) {
	e := newTestEditor()
	e.HandleInput("a")
	e.HandleInput("b")
	e.HandleInput("c")
	e.HandleInput(keyBackspace)
	e.HandleInput(keyBackspace)
	if e.GetText() != "a" {
		t.Errorf("expected 'a', got %q", e.GetText())
	}
}

func TestEditor_MultilineNavigation(t *testing.T) {
	e := newTestEditor()
	e.SetText("abc\ndef\nghi")

	line, col := e.GetCursor()
	if line != 2 || col != 3 {
		t.Fatalf("expected (2,3), got (%d,%d)", line, col)
	}

	e.HandleInput(keyUp)
	e.HandleInput(keyUp)
	line, _ = e.GetCursor()
	if line != 0 {
		t.Errorf("expected line 0, got %d", line)
	}

	e.HandleInput(keyDown)
	e.HandleInput(keyDown)
	line, _ = e.GetCursor()
	if line != 2 {
		t.Errorf("expected line 2, got %d", line)
	}
}
