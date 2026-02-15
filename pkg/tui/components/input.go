// Ported from: packages/tui/src/components/input.ts
// Upstream hash: 1caadb2e
package components

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kfet/tau/pkg/tui"
)

// Re-export CursorMarker from tui package for convenience.
var cursorMarker = tui.CursorMarker

// inputState holds undo state for Input.
type inputState struct {
	value  string
	cursor int
}

// KillRing is a circular buffer for killed text (Emacs-style).
type KillRing struct {
	entries []string
	maxSize int
}

// NewKillRing creates a new KillRing.
func NewKillRing() *KillRing {
	return &KillRing{maxSize: 10}
}

// Push adds text to the kill ring.
func (k *KillRing) Push(text string, prepend, accumulate bool) {
	if text == "" {
		return
	}
	if accumulate && len(k.entries) > 0 {
		last := k.entries[len(k.entries)-1]
		if prepend {
			k.entries[len(k.entries)-1] = text + last
		} else {
			k.entries[len(k.entries)-1] = last + text
		}
		return
	}
	k.entries = append(k.entries, text)
	if len(k.entries) > k.maxSize {
		k.entries = k.entries[1:]
	}
}

// Peek returns the top entry.
func (k *KillRing) Peek() string {
	if len(k.entries) == 0 {
		return ""
	}
	return k.entries[len(k.entries)-1]
}

// Rotate rotates the kill ring (for yank-pop).
func (k *KillRing) Rotate() {
	if len(k.entries) <= 1 {
		return
	}
	last := k.entries[len(k.entries)-1]
	copy(k.entries[1:], k.entries[:len(k.entries)-1])
	k.entries[0] = last
}

// Len returns the number of entries.
func (k *KillRing) Len() int {
	return len(k.entries)
}

// UndoStack is a stack of undo states.
type UndoStack[T any] struct {
	items   []T
	maxSize int
}

// NewUndoStack creates a new UndoStack.
func NewUndoStack[T any]() *UndoStack[T] {
	return &UndoStack[T]{maxSize: 100}
}

// Push saves a state.
func (u *UndoStack[T]) Push(state T) {
	u.items = append(u.items, state)
	if len(u.items) > u.maxSize {
		u.items = u.items[1:]
	}
}

// Pop restores the last state.
func (u *UndoStack[T]) Pop() (T, bool) {
	if len(u.items) == 0 {
		var zero T
		return zero, false
	}
	state := u.items[len(u.items)-1]
	u.items = u.items[:len(u.items)-1]
	return state, true
}

// Input is a single-line text input with horizontal scrolling.
type Input struct {
	value   string
	cursor  int
	Focused bool

	OnSubmit func(value string)
	OnEscape func()

	pasteBuffer string
	isInPaste   bool

	killRing   *KillRing
	lastAction string // "kill", "yank", "type-word", or ""
	undoStack  *UndoStack[inputState]
}

// NewInput creates a new Input component.
func NewInput() *Input {
	return &Input{
		killRing:  NewKillRing(),
		undoStack: NewUndoStack[inputState](),
	}
}

// GetValue returns the current input value.
func (inp *Input) GetValue() string {
	return inp.value
}

// SetValue sets the input value and clamps cursor.
func (inp *Input) SetValue(value string) {
	inp.value = value
	if inp.cursor > len(value) {
		inp.cursor = len(value)
	}
}

// HandleInput processes keyboard input.
func (inp *Input) HandleInput(data string) {
	// Handle bracketed paste
	if strings.Contains(data, "\x1b[200~") {
		inp.isInPaste = true
		inp.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}

	if inp.isInPaste {
		inp.pasteBuffer += data
		endIdx := strings.Index(inp.pasteBuffer, "\x1b[201~")
		if endIdx != -1 {
			pasteContent := inp.pasteBuffer[:endIdx]
			inp.handlePaste(pasteContent)
			inp.isInPaste = false
			remaining := inp.pasteBuffer[endIdx+6:]
			inp.pasteBuffer = ""
			if remaining != "" {
				inp.HandleInput(remaining)
			}
		}
		return
	}

	// Escape
	if tui.MatchesKey(data, "escape") || tui.MatchesKey(data, tui.KeyCtrl("c")) {
		if inp.OnEscape != nil {
			inp.OnEscape()
		}
		return
	}

	// Undo (Ctrl+Z)
	if tui.MatchesKey(data, tui.KeyCtrl("z")) {
		inp.undo()
		return
	}

	// Submit (Enter)
	if tui.MatchesKey(data, "enter") || data == "\n" {
		if inp.OnSubmit != nil {
			inp.OnSubmit(inp.value)
		}
		return
	}

	// Backspace
	if tui.MatchesKey(data, "backspace") {
		inp.handleBackspace()
		return
	}

	// Forward delete
	if tui.MatchesKey(data, "delete") {
		inp.handleForwardDelete()
		return
	}

	// Alt+Backspace = delete word backward
	if tui.MatchesKey(data, "alt+backspace") {
		inp.deleteWordBackwards()
		return
	}

	// Alt+D = delete word forward
	if tui.MatchesKey(data, tui.KeyAlt("d")) {
		inp.deleteWordForward()
		return
	}

	// Ctrl+U = delete to line start
	if tui.MatchesKey(data, tui.KeyCtrl("u")) {
		inp.deleteToLineStart()
		return
	}

	// Ctrl+K = delete to line end
	if tui.MatchesKey(data, tui.KeyCtrl("k")) {
		inp.deleteToLineEnd()
		return
	}

	// Ctrl+Y = yank
	if tui.MatchesKey(data, tui.KeyCtrl("y")) {
		inp.yank()
		return
	}

	// Alt+Y = yank-pop
	if tui.MatchesKey(data, tui.KeyAlt("y")) {
		inp.yankPop()
		return
	}

	// Cursor left
	if tui.MatchesKey(data, "left") {
		inp.lastAction = ""
		if inp.cursor > 0 {
			_, size := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
			inp.cursor -= size
		}
		return
	}

	// Cursor right
	if tui.MatchesKey(data, "right") {
		inp.lastAction = ""
		if inp.cursor < len(inp.value) {
			_, size := utf8.DecodeRuneInString(inp.value[inp.cursor:])
			inp.cursor += size
		}
		return
	}

	// Home / Ctrl+A
	if tui.MatchesKey(data, "home") || tui.MatchesKey(data, tui.KeyCtrl("a")) {
		inp.lastAction = ""
		inp.cursor = 0
		return
	}

	// End / Ctrl+E
	if tui.MatchesKey(data, "end") || tui.MatchesKey(data, tui.KeyCtrl("e")) {
		inp.lastAction = ""
		inp.cursor = len(inp.value)
		return
	}

	// Word left (Alt+Left / Ctrl+Left / Alt+B)
	if tui.MatchesKey(data, "alt+left") || tui.MatchesKey(data, "ctrl+left") || tui.MatchesKey(data, tui.KeyAlt("b")) {
		inp.moveWordBackwards()
		return
	}

	// Word right (Alt+Right / Ctrl+Right / Alt+F)
	if tui.MatchesKey(data, "alt+right") || tui.MatchesKey(data, "ctrl+right") || tui.MatchesKey(data, tui.KeyAlt("f")) {
		inp.moveWordForwards()
		return
	}

	// Regular character input - accept printable chars, reject control chars
	hasControl := false
	for _, r := range data {
		if r < 32 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			hasControl = true
			break
		}
	}
	if !hasControl {
		inp.insertCharacter(data)
	}
}

func (inp *Input) insertCharacter(ch string) {
	r, _ := utf8.DecodeRuneInString(ch)
	if tui.IsWhitespaceChar(r) || inp.lastAction != "type-word" {
		inp.pushUndo()
	}
	inp.lastAction = "type-word"
	inp.value = inp.value[:inp.cursor] + ch + inp.value[inp.cursor:]
	inp.cursor += len(ch)
}

func (inp *Input) handleBackspace() {
	inp.lastAction = ""
	if inp.cursor > 0 {
		inp.pushUndo()
		_, size := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
		inp.value = inp.value[:inp.cursor-size] + inp.value[inp.cursor:]
		inp.cursor -= size
	}
}

func (inp *Input) handleForwardDelete() {
	inp.lastAction = ""
	if inp.cursor < len(inp.value) {
		inp.pushUndo()
		_, size := utf8.DecodeRuneInString(inp.value[inp.cursor:])
		inp.value = inp.value[:inp.cursor] + inp.value[inp.cursor+size:]
	}
}

func (inp *Input) deleteToLineStart() {
	if inp.cursor == 0 {
		return
	}
	inp.pushUndo()
	deleted := inp.value[:inp.cursor]
	inp.killRing.Push(deleted, true, inp.lastAction == "kill")
	inp.lastAction = "kill"
	inp.value = inp.value[inp.cursor:]
	inp.cursor = 0
}

func (inp *Input) deleteToLineEnd() {
	if inp.cursor >= len(inp.value) {
		return
	}
	inp.pushUndo()
	deleted := inp.value[inp.cursor:]
	inp.killRing.Push(deleted, false, inp.lastAction == "kill")
	inp.lastAction = "kill"
	inp.value = inp.value[:inp.cursor]
}

func (inp *Input) deleteWordBackwards() {
	if inp.cursor == 0 {
		return
	}
	wasKill := inp.lastAction == "kill"
	inp.pushUndo()
	oldCursor := inp.cursor
	inp.moveWordBackwards()
	deleteFrom := inp.cursor
	inp.cursor = oldCursor
	deleted := inp.value[deleteFrom:inp.cursor]
	inp.killRing.Push(deleted, true, wasKill)
	inp.lastAction = "kill"
	inp.value = inp.value[:deleteFrom] + inp.value[inp.cursor:]
	inp.cursor = deleteFrom
}

func (inp *Input) deleteWordForward() {
	if inp.cursor >= len(inp.value) {
		return
	}
	wasKill := inp.lastAction == "kill"
	inp.pushUndo()
	oldCursor := inp.cursor
	inp.moveWordForwards()
	deleteTo := inp.cursor
	inp.cursor = oldCursor
	deleted := inp.value[inp.cursor:deleteTo]
	inp.killRing.Push(deleted, false, wasKill)
	inp.lastAction = "kill"
	inp.value = inp.value[:inp.cursor] + inp.value[deleteTo:]
}

func (inp *Input) yank() {
	text := inp.killRing.Peek()
	if text == "" {
		return
	}
	inp.pushUndo()
	inp.value = inp.value[:inp.cursor] + text + inp.value[inp.cursor:]
	inp.cursor += len(text)
	inp.lastAction = "yank"
}

func (inp *Input) yankPop() {
	if inp.lastAction != "yank" || inp.killRing.Len() <= 1 {
		return
	}
	inp.pushUndo()
	prev := inp.killRing.Peek()
	inp.value = inp.value[:inp.cursor-len(prev)] + inp.value[inp.cursor:]
	inp.cursor -= len(prev)
	inp.killRing.Rotate()
	text := inp.killRing.Peek()
	inp.value = inp.value[:inp.cursor] + text + inp.value[inp.cursor:]
	inp.cursor += len(text)
	inp.lastAction = "yank"
}

func (inp *Input) pushUndo() {
	inp.undoStack.Push(inputState{value: inp.value, cursor: inp.cursor})
}

func (inp *Input) undo() {
	if state, ok := inp.undoStack.Pop(); ok {
		inp.value = state.value
		inp.cursor = state.cursor
		inp.lastAction = ""
	}
}

func (inp *Input) moveWordBackwards() {
	if inp.cursor == 0 {
		return
	}
	inp.lastAction = ""

	// Skip trailing whitespace
	for inp.cursor > 0 {
		r, size := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
		if !unicode.IsSpace(r) {
			break
		}
		inp.cursor -= size
	}

	if inp.cursor > 0 {
		r, _ := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
		if tui.IsPunctuationChar(r) {
			// Skip punctuation run
			for inp.cursor > 0 {
				r, size := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
				if !tui.IsPunctuationChar(r) {
					break
				}
				inp.cursor -= size
			}
		} else {
			// Skip word run
			for inp.cursor > 0 {
				r, size := utf8.DecodeLastRuneInString(inp.value[:inp.cursor])
				if unicode.IsSpace(r) || tui.IsPunctuationChar(r) {
					break
				}
				inp.cursor -= size
			}
		}
	}
}

func (inp *Input) moveWordForwards() {
	if inp.cursor >= len(inp.value) {
		return
	}
	inp.lastAction = ""

	// Skip leading whitespace
	for inp.cursor < len(inp.value) {
		r, size := utf8.DecodeRuneInString(inp.value[inp.cursor:])
		if !unicode.IsSpace(r) {
			break
		}
		inp.cursor += size
	}

	if inp.cursor < len(inp.value) {
		r, _ := utf8.DecodeRuneInString(inp.value[inp.cursor:])
		if tui.IsPunctuationChar(r) {
			// Skip punctuation run
			for inp.cursor < len(inp.value) {
				r, size := utf8.DecodeRuneInString(inp.value[inp.cursor:])
				if !tui.IsPunctuationChar(r) {
					break
				}
				inp.cursor += size
			}
		} else {
			// Skip word run
			for inp.cursor < len(inp.value) {
				r, size := utf8.DecodeRuneInString(inp.value[inp.cursor:])
				if unicode.IsSpace(r) || tui.IsPunctuationChar(r) {
					break
				}
				inp.cursor += size
			}
		}
	}
}

func (inp *Input) handlePaste(pastedText string) {
	inp.lastAction = ""
	inp.pushUndo()
	cleanText := strings.ReplaceAll(pastedText, "\r\n", "")
	cleanText = strings.ReplaceAll(cleanText, "\r", "")
	cleanText = strings.ReplaceAll(cleanText, "\n", "")
	inp.value = inp.value[:inp.cursor] + cleanText + inp.value[inp.cursor:]
	inp.cursor += len(cleanText)
}

// Invalidate is a no-op for Input.
func (inp *Input) Invalidate() {}

// Render renders the input with a prompt and cursor.
func (inp *Input) Render(width int) []string {
	prompt := "> "
	availableWidth := width - len(prompt)
	if availableWidth <= 0 {
		return []string{prompt}
	}

	var visibleText string
	cursorDisplay := inp.cursor

	if len(inp.value) < availableWidth {
		visibleText = inp.value
	} else {
		scrollWidth := availableWidth
		if inp.cursor == len(inp.value) {
			scrollWidth = availableWidth - 1
		}
		halfWidth := scrollWidth / 2

		if inp.cursor < halfWidth {
			end := scrollWidth
			if end > len(inp.value) {
				end = len(inp.value)
			}
			visibleText = inp.value[:end]
			cursorDisplay = inp.cursor
		} else if inp.cursor > len(inp.value)-halfWidth {
			start := len(inp.value) - scrollWidth
			if start < 0 {
				start = 0
			}
			visibleText = inp.value[start:]
			cursorDisplay = inp.cursor - start
		} else {
			start := inp.cursor - halfWidth
			end := start + scrollWidth
			if end > len(inp.value) {
				end = len(inp.value)
			}
			visibleText = inp.value[start:end]
			cursorDisplay = halfWidth
		}
	}

	// Build cursor display
	beforeCursor := visibleText[:cursorDisplay]
	atCursor := " "
	afterCursor := ""
	if cursorDisplay < len(visibleText) {
		_, size := utf8.DecodeRuneInString(visibleText[cursorDisplay:])
		atCursor = visibleText[cursorDisplay : cursorDisplay+size]
		afterCursor = visibleText[cursorDisplay+size:]
	}

	marker := ""
	if inp.Focused {
		marker = cursorMarker
	}

	// Reverse video for cursor
	cursorChar := "\x1b[7m" + atCursor + "\x1b[27m"
	textWithCursor := beforeCursor + marker + cursorChar + afterCursor

	visLen := tui.VisibleWidth(textWithCursor)
	padding := ""
	if availableWidth > visLen {
		padding = strings.Repeat(" ", availableWidth-visLen)
	}

	return []string{prompt + textWithCursor + padding}
}
