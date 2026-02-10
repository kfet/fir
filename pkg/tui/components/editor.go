// Ported from: packages/tui/src/components/editor.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kfet/pi-go/pkg/tui"
)

// ---------------------------------------------------------------------------
// Word-wrap helpers
// ---------------------------------------------------------------------------

// TextChunk represents a chunk of text for word-wrap layout.
type TextChunk struct {
	Text       string
	StartIndex int
	EndIndex   int
}

// WordWrapLine splits a line into word-wrapped chunks.
// Wraps at word boundaries when possible, falling back to character-level
// wrapping for words longer than the available width.
func WordWrapLine(line string, maxWidth int) []TextChunk {
	if line == "" || maxWidth <= 0 {
		return []TextChunk{{Text: "", StartIndex: 0, EndIndex: 0}}
	}

	lineWidth := tui.VisibleWidth(line)
	if lineWidth <= maxWidth {
		return []TextChunk{{Text: line, StartIndex: 0, EndIndex: len(line)}}
	}

	var chunks []TextChunk
	graphemes := segmentGraphemes(line)

	currentWidth := 0
	chunkStart := 0
	wrapOppIndex := -1
	wrapOppWidth := 0

	for i := 0; i < len(graphemes); i++ {
		seg := graphemes[i]
		gWidth := tui.VisibleWidth(seg.text)
		isWs := isWhitespace(seg.text)

		// Overflow check before advancing
		if currentWidth+gWidth > maxWidth {
			if wrapOppIndex >= 0 {
				// Backtrack to last wrap opportunity
				chunks = append(chunks, TextChunk{
					Text:       line[chunkStart:wrapOppIndex],
					StartIndex: chunkStart,
					EndIndex:   wrapOppIndex,
				})
				chunkStart = wrapOppIndex
				currentWidth -= wrapOppWidth
			} else if chunkStart < seg.byteOffset {
				// No wrap opportunity: force-break at current position
				chunks = append(chunks, TextChunk{
					Text:       line[chunkStart:seg.byteOffset],
					StartIndex: chunkStart,
					EndIndex:   seg.byteOffset,
				})
				chunkStart = seg.byteOffset
				currentWidth = 0
			}
			wrapOppIndex = -1
		}

		currentWidth += gWidth

		// Record wrap opportunity
		if isWs && i+1 < len(graphemes) && !isWhitespace(graphemes[i+1].text) {
			wrapOppIndex = graphemes[i+1].byteOffset
			wrapOppWidth = currentWidth
		}
	}

	// Push final chunk
	chunks = append(chunks, TextChunk{
		Text:       line[chunkStart:],
		StartIndex: chunkStart,
		EndIndex:   len(line),
	})

	return chunks
}

// grapheme holds a single grapheme's text and its byte offset in the source string.
type grapheme struct {
	text       string
	byteOffset int
}

// segmentGraphemes splits a string into graphemes (using rune boundaries).
func segmentGraphemes(s string) []grapheme {
	var result []grapheme
	offset := 0
	for offset < len(s) {
		r, size := utf8.DecodeRuneInString(s[offset:])
		_ = r
		result = append(result, grapheme{text: s[offset : offset+size], byteOffset: offset})
		offset += size
	}
	return result
}

func isWhitespace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return len(s) > 0
}

// ---------------------------------------------------------------------------
// Kitty CSI-u printable decoding
// ---------------------------------------------------------------------------

var kittyCsiURe = regexp.MustCompile(`^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$`)

const (
	kittyModShift = 1
	kittyModAlt   = 2
	kittyModCtrl  = 4
)

func decodeKittyPrintable(data string) (string, bool) {
	m := kittyCsiURe.FindStringSubmatch(data)
	if m == nil {
		return "", false
	}

	codepoint := parseInt(m[1], -1)
	if codepoint < 0 {
		return "", false
	}

	shiftedKey := -1
	if m[2] != "" {
		shiftedKey = parseInt(m[2], -1)
	}

	modValue := 1
	if m[4] != "" {
		modValue = parseInt(m[4], 1)
	}
	modifier := modValue - 1

	// Ignore CSI-u sequences used for Alt/Ctrl shortcuts
	if modifier&(kittyModAlt|kittyModCtrl) != 0 {
		return "", false
	}

	effectiveCodepoint := codepoint
	if modifier&kittyModShift != 0 && shiftedKey >= 0 {
		effectiveCodepoint = shiftedKey
	}
	if effectiveCodepoint < 32 {
		return "", false
	}

	return string(rune(effectiveCodepoint)), true
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// ---------------------------------------------------------------------------
// Editor keybindings
// ---------------------------------------------------------------------------

// EditorAction is a keybinding action for the editor.
type EditorAction string

const (
	ActCursorUp        EditorAction = "cursorUp"
	ActCursorDown      EditorAction = "cursorDown"
	ActCursorLeft      EditorAction = "cursorLeft"
	ActCursorRight     EditorAction = "cursorRight"
	ActCursorWordLeft  EditorAction = "cursorWordLeft"
	ActCursorWordRight EditorAction = "cursorWordRight"
	ActCursorLineStart EditorAction = "cursorLineStart"
	ActCursorLineEnd   EditorAction = "cursorLineEnd"
	ActJumpForward     EditorAction = "jumpForward"
	ActJumpBackward    EditorAction = "jumpBackward"
	ActPageUp          EditorAction = "pageUp"
	ActPageDown        EditorAction = "pageDown"

	ActDeleteCharBackward EditorAction = "deleteCharBackward"
	ActDeleteCharForward  EditorAction = "deleteCharForward"
	ActDeleteWordBackward EditorAction = "deleteWordBackward"
	ActDeleteWordForward  EditorAction = "deleteWordForward"
	ActDeleteToLineStart  EditorAction = "deleteToLineStart"
	ActDeleteToLineEnd    EditorAction = "deleteToLineEnd"

	ActNewLine EditorAction = "newLine"
	ActSubmit  EditorAction = "submit"
	ActTab     EditorAction = "tab"

	ActSelectUp      EditorAction = "selectUp"
	ActSelectDown    EditorAction = "selectDown"
	ActSelectConfirm EditorAction = "selectConfirm"
	ActSelectCancel  EditorAction = "selectCancel"

	ActCopy    EditorAction = "copy"
	ActYank    EditorAction = "yank"
	ActYankPop EditorAction = "yankPop"
	ActUndo    EditorAction = "undo"

	// UI actions (not directly used by editor, but referenced by keybinding hints)
	ActExpandTools EditorAction = "expandTools"
)

// defaultEditorKeys maps editor actions to default key bindings.
var defaultEditorKeys = map[EditorAction][]tui.KeyID{
	ActCursorUp:        {"up"},
	ActCursorDown:      {"down"},
	ActCursorLeft:      {"left", "ctrl+b"},
	ActCursorRight:     {"right", "ctrl+f"},
	ActCursorWordLeft:  {"alt+left", "ctrl+left", "alt+b"},
	ActCursorWordRight: {"alt+right", "ctrl+right", "alt+f"},
	ActCursorLineStart: {"home", "ctrl+a"},
	ActCursorLineEnd:   {"end", "ctrl+e"},
	ActJumpForward:     {"ctrl+]"},
	ActJumpBackward:    {"ctrl+alt+]"},
	ActPageUp:          {"pageUp"},
	ActPageDown:        {"pageDown"},

	ActDeleteCharBackward: {"backspace"},
	ActDeleteCharForward:  {"delete", "ctrl+d"},
	ActDeleteWordBackward: {"ctrl+w", "alt+backspace"},
	ActDeleteWordForward:  {"alt+d", "alt+delete"},
	ActDeleteToLineStart:  {"ctrl+u"},
	ActDeleteToLineEnd:    {"ctrl+k"},

	ActNewLine: {"shift+enter"},
	ActSubmit:  {"enter"},
	ActTab:     {"tab"},

	ActSelectUp:      {"up"},
	ActSelectDown:    {"down"},
	ActSelectConfirm: {"enter"},
	ActSelectCancel:  {"escape", "ctrl+c"},

	ActCopy:    {"ctrl+c"},
	ActYank:    {"ctrl+y"},
	ActYankPop: {"alt+y"},
	ActUndo:    {"ctrl+-"},

	ActExpandTools: {"ctrl+o"},
}

// editorKeybindings is the global editor keybinding manager.
type editorKeybindings struct {
	actionToKeys map[EditorAction][]tui.KeyID
}

var globalEditorKB = newEditorKeybindings()

func newEditorKeybindings() *editorKeybindings {
	kb := &editorKeybindings{actionToKeys: make(map[EditorAction][]tui.KeyID)}
	for action, keys := range defaultEditorKeys {
		cp := make([]tui.KeyID, len(keys))
		copy(cp, keys)
		kb.actionToKeys[action] = cp
	}
	return kb
}

func (kb *editorKeybindings) matches(data string, action EditorAction) bool {
	keys := kb.actionToKeys[action]
	for _, k := range keys {
		if tui.MatchesKey(data, k) {
			return true
		}
	}
	return false
}

func (kb *editorKeybindings) getKeys(action EditorAction) []tui.KeyID {
	return kb.actionToKeys[action]
}

// GetEditorKeys returns the key bindings for an editor action from the global bindings.
func GetEditorKeys(action EditorAction) []tui.KeyID {
	return globalEditorKB.getKeys(action)
}

// MatchesEditorAction checks if terminal input data matches an editor action.
func MatchesEditorAction(data string, action EditorAction) bool {
	return globalEditorKB.matches(data, action)
}

// ---------------------------------------------------------------------------
// AutocompleteProvider (interfaces for pluggable autocomplete)
// ---------------------------------------------------------------------------

// AutocompleteSuggestions holds autocomplete results.
type AutocompleteSuggestions struct {
	Prefix string
	Items  []SelectItem
}

// ApplyCompletionResult is the result of applying a completion.
type ApplyCompletionResult struct {
	Lines      []string
	CursorLine int
	CursorCol  int
}

// AutocompleteItem is an alias for SelectItem in the autocomplete context.
type AutocompleteItem = SelectItem

// AutocompleteResult is an alias for ApplyCompletionResult.
type AutocompleteResult = ApplyCompletionResult

// AutocompleteProvider provides autocomplete suggestions.
type AutocompleteProvider interface {
	GetSuggestions(lines []string, cursorLine, cursorCol int) *AutocompleteSuggestions
	ApplyCompletion(lines []string, cursorLine, cursorCol int, item SelectItem, prefix string) ApplyCompletionResult
}

// ForceFileSuggestionsProvider can provide "force" file suggestions (on tab).
type ForceFileSuggestionsProvider interface {
	GetForceFileSuggestions(lines []string, cursorLine, cursorCol int) *AutocompleteSuggestions
	ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool
}

// ---------------------------------------------------------------------------
// EditorTheme
// ---------------------------------------------------------------------------

// EditorTheme provides styling for the editor.
type EditorTheme struct {
	BorderColor func(string) string
	SelectList  SelectListTheme
}

// EditorOptions holds editor configuration.
type EditorOptions struct {
	PaddingX              int
	AutocompleteMaxVisible int
}

// ---------------------------------------------------------------------------
// Editor state
// ---------------------------------------------------------------------------

type editorState struct {
	lines      []string
	cursorLine int
	cursorCol  int
}

func (s editorState) clone() editorState {
	lines := make([]string, len(s.lines))
	copy(lines, s.lines)
	return editorState{lines: lines, cursorLine: s.cursorLine, cursorCol: s.cursorCol}
}

type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int // only valid when hasCursor is true
}

// ---------------------------------------------------------------------------
// Editor component
// ---------------------------------------------------------------------------

// Editor is a multi-line text editor component with word-wrap, undo,
// kill-ring, autocomplete, history, and more.
type Editor struct {
	state editorState

	Focused bool

	tuiRef      *tui.TUI
	theme       EditorTheme
	paddingX    int
	lastWidth   int
	scrollOffset int
	BorderColor func(string) string

	autocompleteProvider AutocompleteProvider
	autocompleteList     *SelectList
	autocompleteState    string // "", "regular", or "force"
	autocompletePrefix   string
	autocompleteMaxVis   int

	pastes       map[int]string
	pasteCounter int
	pasteBuffer  string
	isInPaste    bool

	history      []string
	historyIndex int // -1 = not browsing

	killRing   *KillRing
	lastAction string // "kill", "yank", "type-word", ""

	jumpMode string // "forward", "backward", ""

	preferredVisualCol *int

	undoStack *UndoStack[editorState]

	OnSubmit      func(text string)
	OnChange      func(text string)
	DisableSubmit bool
}

// NewEditor creates a new Editor.
func NewEditor(t *tui.TUI, theme EditorTheme, opts ...EditorOptions) *Editor {
	var o EditorOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	paddingX := o.PaddingX
	if paddingX < 0 {
		paddingX = 0
	}
	maxVis := o.AutocompleteMaxVisible
	if maxVis == 0 {
		maxVis = 5
	}
	if maxVis < 3 {
		maxVis = 3
	}
	if maxVis > 20 {
		maxVis = 20
	}
	return &Editor{
		state:            editorState{lines: []string{""}},
		tuiRef:           t,
		theme:            theme,
		paddingX:         paddingX,
		lastWidth:        80,
		BorderColor:      theme.BorderColor,
		autocompleteMaxVis: maxVis,
		pastes:           make(map[int]string),
		historyIndex:     -1,
		killRing:         NewKillRing(),
		undoStack:        NewUndoStack[editorState](),
	}
}

// GetPaddingX returns the horizontal padding.
func (e *Editor) GetPaddingX() int { return e.paddingX }

// SetPaddingX sets the horizontal padding.
func (e *Editor) SetPaddingX(p int) {
	if p < 0 {
		p = 0
	}
	if e.paddingX != p {
		e.paddingX = p
		if e.tuiRef != nil {
			e.tuiRef.RequestRender(false)
		}
	}
}

// GetAutocompleteMaxVisible returns the max visible autocomplete items.
func (e *Editor) GetAutocompleteMaxVisible() int { return e.autocompleteMaxVis }

// SetAutocompleteMaxVisible sets the max visible autocomplete items (clamped 3..20).
func (e *Editor) SetAutocompleteMaxVisible(n int) {
	if n < 3 {
		n = 3
	}
	if n > 20 {
		n = 20
	}
	e.autocompleteMaxVis = n
}

// SetAutocompleteProvider sets the autocomplete provider.
func (e *Editor) SetAutocompleteProvider(p AutocompleteProvider) {
	e.autocompleteProvider = p
}

// AddToHistory adds a prompt to history for up/down arrow navigation.
func (e *Editor) AddToHistory(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	if len(e.history) > 0 && e.history[0] == trimmed {
		return
	}
	e.history = append([]string{trimmed}, e.history...)
	if len(e.history) > 100 {
		e.history = e.history[:100]
	}
}

// GetText returns the full editor text.
func (e *Editor) GetText() string {
	return strings.Join(e.state.lines, "\n")
}

// GetExpandedText returns text with paste markers expanded.
func (e *Editor) GetExpandedText() string {
	result := strings.Join(e.state.lines, "\n")
	for pasteID, pasteContent := range e.pastes {
		re := regexp.MustCompile(fmt.Sprintf(`\[paste #%d( (\+\d+ lines|\d+ chars))?\]`, pasteID))
		result = re.ReplaceAllString(result, pasteContent)
	}
	return result
}

// GetLines returns a copy of the editor lines.
func (e *Editor) GetLines() []string {
	cp := make([]string, len(e.state.lines))
	copy(cp, e.state.lines)
	return cp
}

// GetCursor returns the cursor position.
func (e *Editor) GetCursor() (line, col int) {
	return e.state.cursorLine, e.state.cursorCol
}

// SetText sets the editor text, resetting history browsing.
func (e *Editor) SetText(text string) {
	e.lastAction = ""
	e.historyIndex = -1
	if e.GetText() != text {
		e.pushUndoSnapshot()
	}
	e.setTextInternal(text)
}

// InsertTextAtCursor inserts text at the cursor position.
func (e *Editor) InsertTextAtCursor(text string) {
	if text == "" {
		return
	}
	e.pushUndoSnapshot()
	e.lastAction = ""
	e.historyIndex = -1
	e.insertTextAtCursorInternal(text)
}

// IsShowingAutocomplete returns whether autocomplete is visible.
func (e *Editor) IsShowingAutocomplete() bool {
	return e.autocompleteState != ""
}

// SetFocused sets the focus state.
func (e *Editor) SetFocused(focused bool) {
	e.Focused = focused
}

// Invalidate is a no-op for Editor.
func (e *Editor) Invalidate() {}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (e *Editor) setTextInternal(text string) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	e.state.lines = lines
	e.state.cursorLine = len(lines) - 1
	e.setCursorCol(len(lines[len(lines)-1]))
	e.scrollOffset = 0

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) setCursorCol(col int) {
	e.state.cursorCol = col
	e.preferredVisualCol = nil
}

func (e *Editor) isEditorEmpty() bool {
	return len(e.state.lines) == 1 && e.state.lines[0] == ""
}

func (e *Editor) isOnFirstVisualLine() bool {
	visualLines := e.buildVisualLineMap(e.lastWidth)
	return e.findCurrentVisualLine(visualLines) == 0
}

func (e *Editor) isOnLastVisualLine() bool {
	visualLines := e.buildVisualLineMap(e.lastWidth)
	return e.findCurrentVisualLine(visualLines) == len(visualLines)-1
}

func (e *Editor) pushUndoSnapshot() {
	e.undoStack.Push(e.state.clone())
}

func (e *Editor) undo() {
	e.historyIndex = -1
	snapshot, ok := e.undoStack.Pop()
	if !ok {
		return
	}
	e.state = snapshot
	e.lastAction = ""
	e.preferredVisualCol = nil
	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

// ---------------------------------------------------------------------------
// History navigation
// ---------------------------------------------------------------------------

func (e *Editor) navigateHistory(direction int) {
	e.lastAction = ""
	if len(e.history) == 0 {
		return
	}

	newIndex := e.historyIndex - direction // Up(-1) increases index
	if newIndex < -1 || newIndex >= len(e.history) {
		return
	}

	if e.historyIndex == -1 && newIndex >= 0 {
		e.pushUndoSnapshot()
	}

	e.historyIndex = newIndex

	if e.historyIndex == -1 {
		e.setTextInternal("")
	} else {
		e.setTextInternal(e.history[e.historyIndex])
	}
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------

// Render renders the editor to a list of lines.
func (e *Editor) Render(width int) []string {
	maxPadding := int(math.Max(0, math.Floor(float64(width-1)/2.0)))
	paddingX := e.paddingX
	if paddingX > maxPadding {
		paddingX = maxPadding
	}
	contentWidth := width - paddingX*2
	if contentWidth < 1 {
		contentWidth = 1
	}

	layoutWidth := contentWidth
	if paddingX == 0 {
		layoutWidth = contentWidth - 1
	}
	if layoutWidth < 1 {
		layoutWidth = 1
	}
	e.lastWidth = layoutWidth

	horizontal := e.BorderColor("─")

	layoutLines := e.layoutText(layoutWidth)

	termRows := 24
	if e.tuiRef != nil {
		termRows = e.tuiRef.Terminal.Rows()
	}
	maxVisibleLines := int(math.Max(5, math.Floor(float64(termRows)*0.3)))

	cursorLineIndex := 0
	for i, ll := range layoutLines {
		if ll.hasCursor {
			cursorLineIndex = i
			break
		}
	}

	// Adjust scroll offset to keep cursor visible
	if cursorLineIndex < e.scrollOffset {
		e.scrollOffset = cursorLineIndex
	} else if cursorLineIndex >= e.scrollOffset+maxVisibleLines {
		e.scrollOffset = cursorLineIndex - maxVisibleLines + 1
	}

	maxScrollOffset := len(layoutLines) - maxVisibleLines
	if maxScrollOffset < 0 {
		maxScrollOffset = 0
	}
	if e.scrollOffset < 0 {
		e.scrollOffset = 0
	}
	if e.scrollOffset > maxScrollOffset {
		e.scrollOffset = maxScrollOffset
	}

	end := e.scrollOffset + maxVisibleLines
	if end > len(layoutLines) {
		end = len(layoutLines)
	}
	visibleLines := layoutLines[e.scrollOffset:end]

	var result []string
	leftPadding := strings.Repeat(" ", paddingX)
	rightPadding := leftPadding

	// Top border
	if e.scrollOffset > 0 {
		indicator := fmt.Sprintf("─── ↑ %d more ", e.scrollOffset)
		remaining := width - tui.VisibleWidth(indicator)
		if remaining < 0 {
			remaining = 0
		}
		result = append(result, e.BorderColor(indicator+strings.Repeat("─", remaining)))
	} else {
		result = append(result, strings.Repeat(horizontal, width))
	}

	emitCursorMarker := e.Focused && e.autocompleteState == ""

	for _, ll := range visibleLines {
		displayText := ll.text
		lineVisWidth := tui.VisibleWidth(ll.text)
		cursorInPadding := false

		if ll.hasCursor {
			before := displayText[:ll.cursorPos]
			after := displayText[ll.cursorPos:]

			marker := ""
			if emitCursorMarker {
				marker = tui.CursorMarker
			}

			if len(after) > 0 {
				// Cursor on a character
				firstR, firstSize := utf8.DecodeRuneInString(after)
				firstGrapheme := string(firstR)
				_ = firstSize
				restAfter := after[firstSize:]
				cursor := "\x1b[7m" + firstGrapheme + "\x1b[0m"
				displayText = before + marker + cursor + restAfter
			} else {
				// Cursor at end
				cursor := "\x1b[7m \x1b[0m"
				displayText = before + marker + cursor
				lineVisWidth++
				if lineVisWidth > contentWidth && paddingX > 0 {
					cursorInPadding = true
				}
			}
		}

		padding := strings.Repeat(" ", max(0, contentWidth-lineVisWidth))
		rp := rightPadding
		if cursorInPadding && len(rp) > 0 {
			rp = rp[1:]
		}
		result = append(result, leftPadding+displayText+padding+rp)
	}

	// Bottom border
	linesBelow := len(layoutLines) - (e.scrollOffset + len(visibleLines))
	if linesBelow > 0 {
		indicator := fmt.Sprintf("─── ↓ %d more ", linesBelow)
		remaining := width - tui.VisibleWidth(indicator)
		if remaining < 0 {
			remaining = 0
		}
		result = append(result, e.BorderColor(indicator+strings.Repeat("─", remaining)))
	} else {
		result = append(result, strings.Repeat(horizontal, width))
	}

	// Autocomplete list
	if e.autocompleteState != "" && e.autocompleteList != nil {
		acLines := e.autocompleteList.Render(contentWidth)
		for _, acl := range acLines {
			lw := tui.VisibleWidth(acl)
			pad := strings.Repeat(" ", max(0, contentWidth-lw))
			result = append(result, leftPadding+acl+pad+rightPadding)
		}
	}

	return result
}

func (e *Editor) layoutText(contentWidth int) []layoutLine {
	var result []layoutLine

	if len(e.state.lines) == 0 || (len(e.state.lines) == 1 && e.state.lines[0] == "") {
		result = append(result, layoutLine{text: "", hasCursor: true, cursorPos: 0})
		return result
	}

	for i, line := range e.state.lines {
		isCurrentLine := i == e.state.cursorLine
		lineVisWidth := tui.VisibleWidth(line)

		if lineVisWidth <= contentWidth {
			if isCurrentLine {
				result = append(result, layoutLine{text: line, hasCursor: true, cursorPos: e.state.cursorCol})
			} else {
				result = append(result, layoutLine{text: line})
			}
		} else {
			chunks := WordWrapLine(line, contentWidth)
			for ci, chunk := range chunks {
				isLast := ci == len(chunks)-1
				hasCursorInChunk := false
				adjustedCursorPos := 0

				if isCurrentLine {
					cursorPos := e.state.cursorCol
					if isLast {
						hasCursorInChunk = cursorPos >= chunk.StartIndex
						adjustedCursorPos = cursorPos - chunk.StartIndex
					} else {
						hasCursorInChunk = cursorPos >= chunk.StartIndex && cursorPos < chunk.EndIndex
						if hasCursorInChunk {
							adjustedCursorPos = cursorPos - chunk.StartIndex
							if adjustedCursorPos > len(chunk.Text) {
								adjustedCursorPos = len(chunk.Text)
							}
						}
					}
				}

				if hasCursorInChunk {
					result = append(result, layoutLine{text: chunk.Text, hasCursor: true, cursorPos: adjustedCursorPos})
				} else {
					result = append(result, layoutLine{text: chunk.Text})
				}
			}
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Visual line map (for vertical cursor navigation)
// ---------------------------------------------------------------------------

type visualLine struct {
	logicalLine int
	startCol    int
	length      int
}

func (e *Editor) buildVisualLineMap(width int) []visualLine {
	var vls []visualLine
	for i, line := range e.state.lines {
		if line == "" {
			vls = append(vls, visualLine{logicalLine: i, startCol: 0, length: 0})
		} else if tui.VisibleWidth(line) <= width {
			vls = append(vls, visualLine{logicalLine: i, startCol: 0, length: len(line)})
		} else {
			chunks := WordWrapLine(line, width)
			for _, c := range chunks {
				vls = append(vls, visualLine{logicalLine: i, startCol: c.StartIndex, length: c.EndIndex - c.StartIndex})
			}
		}
	}
	return vls
}

func (e *Editor) findCurrentVisualLine(vls []visualLine) int {
	for i, vl := range vls {
		if vl.logicalLine == e.state.cursorLine {
			colInSeg := e.state.cursorCol - vl.startCol
			isLast := i == len(vls)-1 || vls[i+1].logicalLine != vl.logicalLine
			if colInSeg >= 0 && (colInSeg < vl.length || (isLast && colInSeg <= vl.length)) {
				return i
			}
		}
	}
	return len(vls) - 1
}

// ---------------------------------------------------------------------------
// HandleInput
// ---------------------------------------------------------------------------

// HandleInput processes a keyboard input event.
func (e *Editor) HandleInput(data string) {
	kb := globalEditorKB

	// Handle character jump mode
	if e.jumpMode != "" {
		if kb.matches(data, ActJumpForward) || kb.matches(data, ActJumpBackward) {
			e.jumpMode = ""
			return
		}
		if len(data) > 0 && data[0] >= 32 {
			direction := e.jumpMode
			e.jumpMode = ""
			e.jumpToChar(data, direction)
			return
		}
		e.jumpMode = ""
	}

	// Bracketed paste mode
	if strings.Contains(data, "\x1b[200~") {
		e.isInPaste = true
		e.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}

	if e.isInPaste {
		e.pasteBuffer += data
		endIdx := strings.Index(e.pasteBuffer, "\x1b[201~")
		if endIdx != -1 {
			pasteContent := e.pasteBuffer[:endIdx]
			if pasteContent != "" {
				e.handlePaste(pasteContent)
			}
			e.isInPaste = false
			remaining := e.pasteBuffer[endIdx+6:]
			e.pasteBuffer = ""
			if remaining != "" {
				e.HandleInput(remaining)
			}
			return
		}
		return
	}

	// Ctrl+C - let parent handle
	if kb.matches(data, ActCopy) {
		return
	}

	// Undo
	if kb.matches(data, ActUndo) {
		e.undo()
		return
	}

	// Autocomplete mode handling
	if e.autocompleteState != "" && e.autocompleteList != nil {
		if kb.matches(data, ActSelectCancel) {
			e.cancelAutocomplete()
			return
		}
		if kb.matches(data, ActSelectUp) || kb.matches(data, ActSelectDown) {
			e.autocompleteList.HandleInput(data)
			return
		}
		if kb.matches(data, ActTab) {
			selected := e.autocompleteList.GetSelectedItem()
			if selected != nil && e.autocompleteProvider != nil {
				e.pushUndoSnapshot()
				e.lastAction = ""
				result := e.autocompleteProvider.ApplyCompletion(
					e.state.lines, e.state.cursorLine, e.state.cursorCol,
					*selected, e.autocompletePrefix,
				)
				e.state.lines = result.Lines
				e.state.cursorLine = result.CursorLine
				e.setCursorCol(result.CursorCol)
				e.cancelAutocomplete()
				if e.OnChange != nil {
					e.OnChange(e.GetText())
				}
			}
			return
		}
		if kb.matches(data, ActSelectConfirm) {
			selected := e.autocompleteList.GetSelectedItem()
			if selected != nil && e.autocompleteProvider != nil {
				e.pushUndoSnapshot()
				e.lastAction = ""
				result := e.autocompleteProvider.ApplyCompletion(
					e.state.lines, e.state.cursorLine, e.state.cursorCol,
					*selected, e.autocompletePrefix,
				)
				e.state.lines = result.Lines
				e.state.cursorLine = result.CursorLine
				e.setCursorCol(result.CursorCol)

				if strings.HasPrefix(e.autocompletePrefix, "/") {
					e.cancelAutocomplete()
					// Fall through to submit below
				} else {
					e.cancelAutocomplete()
					if e.OnChange != nil {
						e.OnChange(e.GetText())
					}
					return
				}
			}
		}
	}

	// Tab completion
	if kb.matches(data, ActTab) && e.autocompleteState == "" {
		e.handleTabCompletion()
		return
	}

	// Deletion actions
	if kb.matches(data, ActDeleteToLineEnd) {
		e.deleteToEndOfLine()
		return
	}
	if kb.matches(data, ActDeleteToLineStart) {
		e.deleteToStartOfLine()
		return
	}
	if kb.matches(data, ActDeleteWordBackward) {
		e.deleteWordBackwards()
		return
	}
	if kb.matches(data, ActDeleteWordForward) {
		e.deleteWordForward()
		return
	}
	if kb.matches(data, ActDeleteCharBackward) || tui.MatchesKey(data, "shift+backspace") {
		e.handleBackspace()
		return
	}
	if kb.matches(data, ActDeleteCharForward) || tui.MatchesKey(data, "shift+delete") {
		e.handleForwardDelete()
		return
	}

	// Kill ring
	if kb.matches(data, ActYank) {
		e.yank()
		return
	}
	if kb.matches(data, ActYankPop) {
		e.yankPop()
		return
	}

	// Cursor movement
	if kb.matches(data, ActCursorLineStart) {
		e.moveToLineStart()
		return
	}
	if kb.matches(data, ActCursorLineEnd) {
		e.moveToLineEnd()
		return
	}
	if kb.matches(data, ActCursorWordLeft) {
		e.moveWordBackwards()
		return
	}
	if kb.matches(data, ActCursorWordRight) {
		e.moveWordForwards()
		return
	}

	// New line (Shift+Enter and other newline sequences)
	if kb.matches(data, ActNewLine) ||
		(data[0] == 10 && len(data) > 1) ||
		data == "\x1b\r" ||
		data == "\x1b[13;2~" ||
		(len(data) > 1 && strings.Contains(data, "\x1b") && strings.Contains(data, "\r")) ||
		(data == "\n" && len(data) == 1) {
		if e.shouldSubmitOnBackslashEnter(data, kb) {
			e.handleBackspace()
			e.submitValue()
			return
		}
		e.addNewLine()
		return
	}

	// Submit (Enter)
	if kb.matches(data, ActSubmit) {
		if e.DisableSubmit {
			return
		}
		// Backslash+Enter workaround
		currentLine := e.state.lines[e.state.cursorLine]
		if e.state.cursorCol > 0 && e.state.cursorCol <= len(currentLine) && currentLine[e.state.cursorCol-1] == '\\' {
			e.handleBackspace()
			e.addNewLine()
			return
		}
		e.submitValue()
		return
	}

	// Arrow keys with history support
	if kb.matches(data, ActCursorUp) {
		if e.isEditorEmpty() {
			e.navigateHistory(-1)
		} else if e.historyIndex > -1 && e.isOnFirstVisualLine() {
			e.navigateHistory(-1)
		} else if e.isOnFirstVisualLine() {
			e.moveToLineStart()
		} else {
			e.moveCursor(-1, 0)
		}
		return
	}
	if kb.matches(data, ActCursorDown) {
		if e.historyIndex > -1 && e.isOnLastVisualLine() {
			e.navigateHistory(1)
		} else if e.isOnLastVisualLine() {
			e.moveToLineEnd()
		} else {
			e.moveCursor(1, 0)
		}
		return
	}
	if kb.matches(data, ActCursorRight) {
		e.moveCursor(0, 1)
		return
	}
	if kb.matches(data, ActCursorLeft) {
		e.moveCursor(0, -1)
		return
	}

	// Page up/down
	if kb.matches(data, ActPageUp) {
		e.pageScroll(-1)
		return
	}
	if kb.matches(data, ActPageDown) {
		e.pageScroll(1)
		return
	}

	// Jump mode triggers
	if kb.matches(data, ActJumpForward) {
		e.jumpMode = "forward"
		return
	}
	if kb.matches(data, ActJumpBackward) {
		e.jumpMode = "backward"
		return
	}

	// Shift+Space
	if tui.MatchesKey(data, "shift+space") {
		e.insertCharacter(" ", false)
		return
	}

	// Kitty CSI-u printable
	if ch, ok := decodeKittyPrintable(data); ok {
		e.insertCharacter(ch, false)
		return
	}

	// Regular printable characters
	if len(data) > 0 && data[0] >= 32 {
		e.insertCharacter(data, false)
	}
}

// ---------------------------------------------------------------------------
// Text mutation methods
// ---------------------------------------------------------------------------

func (e *Editor) insertCharacter(ch string, skipUndoCoalescing bool) {
	e.historyIndex = -1

	if !skipUndoCoalescing {
		r, _ := utf8.DecodeRuneInString(ch)
		if unicode.IsSpace(r) || e.lastAction != "type-word" {
			e.pushUndoSnapshot()
		}
		e.lastAction = "type-word"
	}

	line := e.state.lines[e.state.cursorLine]
	before := line[:e.state.cursorCol]
	after := line[e.state.cursorCol:]
	e.state.lines[e.state.cursorLine] = before + ch + after
	e.setCursorCol(e.state.cursorCol + len(ch))

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}

	// Auto-trigger autocomplete
	if e.autocompleteState == "" {
		if ch == "/" && e.isAtStartOfMessage() {
			e.tryTriggerAutocomplete(false)
		} else if ch == "@" {
			currentLine := e.state.lines[e.state.cursorLine]
			textBeforeCursor := currentLine[:e.state.cursorCol]
			if len(textBeforeCursor) == 1 || (len(textBeforeCursor) >= 2 && (textBeforeCursor[len(textBeforeCursor)-2] == ' ' || textBeforeCursor[len(textBeforeCursor)-2] == '\t')) {
				e.tryTriggerAutocomplete(false)
			}
		} else if isAlnumDotDashUnderscore(ch) {
			currentLine := e.state.lines[e.state.cursorLine]
			textBeforeCursor := currentLine[:e.state.cursorCol]
			if e.isInSlashCommandContext(textBeforeCursor) {
				e.tryTriggerAutocomplete(false)
			} else if matchAtFileRef(textBeforeCursor) {
				e.tryTriggerAutocomplete(false)
			}
		}
	} else {
		e.updateAutocomplete()
	}
}

func (e *Editor) handlePaste(pastedText string) {
	e.historyIndex = -1
	e.lastAction = ""
	e.pushUndoSnapshot()

	cleanText := strings.ReplaceAll(pastedText, "\r\n", "\n")
	cleanText = strings.ReplaceAll(cleanText, "\r", "\n")
	tabExpanded := strings.ReplaceAll(cleanText, "\t", "    ")

	// Filter non-printable chars except newlines
	var filtered strings.Builder
	for _, r := range tabExpanded {
		if r == '\n' || r >= 32 {
			filtered.WriteRune(r)
		}
	}
	filteredText := filtered.String()

	// Prepend space if pasting a file path after a word character
	if len(filteredText) > 0 && (filteredText[0] == '/' || filteredText[0] == '~' || filteredText[0] == '.') {
		currentLine := e.state.lines[e.state.cursorLine]
		if e.state.cursorCol > 0 && e.state.cursorCol <= len(currentLine) {
			charBefore := currentLine[e.state.cursorCol-1]
			if isWordChar(rune(charBefore)) {
				filteredText = " " + filteredText
			}
		}
	}

	pastedLines := strings.Split(filteredText, "\n")
	totalChars := len(filteredText)

	// Large paste → store with marker
	if len(pastedLines) > 10 || totalChars > 1000 {
		e.pasteCounter++
		pasteID := e.pasteCounter
		e.pastes[pasteID] = filteredText

		var marker string
		if len(pastedLines) > 10 {
			marker = fmt.Sprintf("[paste #%d +%d lines]", pasteID, len(pastedLines))
		} else {
			marker = fmt.Sprintf("[paste #%d %d chars]", pasteID, totalChars)
		}
		e.insertTextAtCursorInternal(marker)
		return
	}

	if len(pastedLines) == 1 {
		for _, r := range filteredText {
			e.insertCharacter(string(r), true)
		}
		return
	}

	e.insertTextAtCursorInternal(filteredText)
}

func (e *Editor) insertTextAtCursorInternal(text string) {
	if text == "" {
		return
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	insertedLines := strings.Split(normalized, "\n")

	currentLine := e.state.lines[e.state.cursorLine]
	beforeCursor := currentLine[:e.state.cursorCol]
	afterCursor := currentLine[e.state.cursorCol:]

	if len(insertedLines) == 1 {
		e.state.lines[e.state.cursorLine] = beforeCursor + normalized + afterCursor
		e.setCursorCol(e.state.cursorCol + len(normalized))
	} else {
		// Multi-line
		var newLines []string
		newLines = append(newLines, e.state.lines[:e.state.cursorLine]...)
		newLines = append(newLines, beforeCursor+insertedLines[0])
		newLines = append(newLines, insertedLines[1:len(insertedLines)-1]...)
		newLines = append(newLines, insertedLines[len(insertedLines)-1]+afterCursor)
		newLines = append(newLines, e.state.lines[e.state.cursorLine+1:]...)
		e.state.lines = newLines
		e.state.cursorLine += len(insertedLines) - 1
		e.setCursorCol(len(insertedLines[len(insertedLines)-1]))
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) addNewLine() {
	e.historyIndex = -1
	e.lastAction = ""
	e.pushUndoSnapshot()

	currentLine := e.state.lines[e.state.cursorLine]
	before := currentLine[:e.state.cursorCol]
	after := currentLine[e.state.cursorCol:]

	e.state.lines[e.state.cursorLine] = before
	// Insert after current line
	newLines := make([]string, 0, len(e.state.lines)+1)
	newLines = append(newLines, e.state.lines[:e.state.cursorLine+1]...)
	newLines = append(newLines, after)
	newLines = append(newLines, e.state.lines[e.state.cursorLine+1:]...)
	e.state.lines = newLines

	e.state.cursorLine++
	e.setCursorCol(0)

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) shouldSubmitOnBackslashEnter(data string, kb *editorKeybindings) bool {
	if e.DisableSubmit {
		return false
	}
	if !tui.MatchesKey(data, "enter") {
		return false
	}
	submitKeys := kb.getKeys(ActSubmit)
	hasShiftEnter := false
	for _, k := range submitKeys {
		if k == "shift+enter" || k == "shift+return" {
			hasShiftEnter = true
			break
		}
	}
	if !hasShiftEnter {
		return false
	}
	currentLine := e.state.lines[e.state.cursorLine]
	return e.state.cursorCol > 0 && e.state.cursorCol <= len(currentLine) && currentLine[e.state.cursorCol-1] == '\\'
}

func (e *Editor) submitValue() {
	result := strings.TrimSpace(strings.Join(e.state.lines, "\n"))
	for pasteID, pasteContent := range e.pastes {
		re := regexp.MustCompile(fmt.Sprintf(`\[paste #%d( (\+\d+ lines|\d+ chars))?\]`, pasteID))
		result = re.ReplaceAllString(result, pasteContent)
	}

	e.state = editorState{lines: []string{""}, cursorLine: 0, cursorCol: 0}
	e.pastes = make(map[int]string)
	e.pasteCounter = 0
	e.historyIndex = -1
	e.scrollOffset = 0
	e.undoStack = NewUndoStack[editorState]()
	e.lastAction = ""

	if e.OnChange != nil {
		e.OnChange("")
	}
	if e.OnSubmit != nil {
		e.OnSubmit(result)
	}
}

// ---------------------------------------------------------------------------
// Backspace / delete
// ---------------------------------------------------------------------------

func (e *Editor) handleBackspace() {
	e.historyIndex = -1
	e.lastAction = ""

	if e.state.cursorCol > 0 {
		e.pushUndoSnapshot()
		line := e.state.lines[e.state.cursorLine]
		beforeCursor := line[:e.state.cursorCol]
		_, graphemeLen := utf8.DecodeLastRuneInString(beforeCursor)
		before := line[:e.state.cursorCol-graphemeLen]
		after := line[e.state.cursorCol:]
		e.state.lines[e.state.cursorLine] = before + after
		e.setCursorCol(e.state.cursorCol - graphemeLen)
	} else if e.state.cursorLine > 0 {
		e.pushUndoSnapshot()
		currentLine := e.state.lines[e.state.cursorLine]
		prevLine := e.state.lines[e.state.cursorLine-1]
		e.state.lines[e.state.cursorLine-1] = prevLine + currentLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
		e.state.cursorLine--
		e.setCursorCol(len(prevLine))
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}

	// Update or re-trigger autocomplete
	if e.autocompleteState != "" {
		e.updateAutocomplete()
	} else {
		currentLine := e.state.lines[e.state.cursorLine]
		textBeforeCursor := currentLine[:e.state.cursorCol]
		if e.isInSlashCommandContext(textBeforeCursor) {
			e.tryTriggerAutocomplete(false)
		} else if matchAtFileRef(textBeforeCursor) {
			e.tryTriggerAutocomplete(false)
		}
	}
}

func (e *Editor) handleForwardDelete() {
	e.historyIndex = -1
	e.lastAction = ""

	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol < len(currentLine) {
		e.pushUndoSnapshot()
		afterCursor := currentLine[e.state.cursorCol:]
		_, graphemeLen := utf8.DecodeRuneInString(afterCursor)
		before := currentLine[:e.state.cursorCol]
		after := currentLine[e.state.cursorCol+graphemeLen:]
		e.state.lines[e.state.cursorLine] = before + after
	} else if e.state.cursorLine < len(e.state.lines)-1 {
		e.pushUndoSnapshot()
		nextLine := e.state.lines[e.state.cursorLine+1]
		e.state.lines[e.state.cursorLine] = currentLine + nextLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}

	// Update autocomplete
	if e.autocompleteState != "" {
		e.updateAutocomplete()
	} else {
		currentLine := e.state.lines[e.state.cursorLine]
		textBeforeCursor := currentLine[:e.state.cursorCol]
		if e.isInSlashCommandContext(textBeforeCursor) {
			e.tryTriggerAutocomplete(false)
		} else if matchAtFileRef(textBeforeCursor) {
			e.tryTriggerAutocomplete(false)
		}
	}
}

// ---------------------------------------------------------------------------
// Kill / delete line helpers
// ---------------------------------------------------------------------------

func (e *Editor) deleteToStartOfLine() {
	e.historyIndex = -1
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol > 0 {
		e.pushUndoSnapshot()
		deleted := currentLine[:e.state.cursorCol]
		e.killRing.Push(deleted, true, e.lastAction == "kill")
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[e.state.cursorCol:]
		e.setCursorCol(0)
	} else if e.state.cursorLine > 0 {
		e.pushUndoSnapshot()
		e.killRing.Push("\n", true, e.lastAction == "kill")
		e.lastAction = "kill"
		prevLine := e.state.lines[e.state.cursorLine-1]
		e.state.lines[e.state.cursorLine-1] = prevLine + currentLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
		e.state.cursorLine--
		e.setCursorCol(len(prevLine))
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) deleteToEndOfLine() {
	e.historyIndex = -1
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol < len(currentLine) {
		e.pushUndoSnapshot()
		deleted := currentLine[e.state.cursorCol:]
		e.killRing.Push(deleted, false, e.lastAction == "kill")
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:e.state.cursorCol]
	} else if e.state.cursorLine < len(e.state.lines)-1 {
		e.pushUndoSnapshot()
		e.killRing.Push("\n", false, e.lastAction == "kill")
		e.lastAction = "kill"
		nextLine := e.state.lines[e.state.cursorLine+1]
		e.state.lines[e.state.cursorLine] = currentLine + nextLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) deleteWordBackwards() {
	e.historyIndex = -1
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.pushUndoSnapshot()
			e.killRing.Push("\n", true, e.lastAction == "kill")
			e.lastAction = "kill"
			prevLine := e.state.lines[e.state.cursorLine-1]
			e.state.lines[e.state.cursorLine-1] = prevLine + currentLine
			e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
			e.state.cursorLine--
			e.setCursorCol(len(prevLine))
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == "kill"
		oldCol := e.state.cursorCol
		e.moveWordBackwards()
		deleteFrom := e.state.cursorCol
		e.state.cursorCol = oldCol

		deleted := currentLine[deleteFrom:e.state.cursorCol]
		e.killRing.Push(deleted, true, wasKill)
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:deleteFrom] + currentLine[e.state.cursorCol:]
		e.setCursorCol(deleteFrom)
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) deleteWordForward() {
	e.historyIndex = -1
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol >= len(currentLine) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.pushUndoSnapshot()
			e.killRing.Push("\n", false, e.lastAction == "kill")
			e.lastAction = "kill"
			nextLine := e.state.lines[e.state.cursorLine+1]
			e.state.lines[e.state.cursorLine] = currentLine + nextLine
			e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == "kill"
		oldCol := e.state.cursorCol
		e.moveWordForwards()
		deleteTo := e.state.cursorCol
		e.state.cursorCol = oldCol

		deleted := currentLine[e.state.cursorCol:deleteTo]
		e.killRing.Push(deleted, false, wasKill)
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:e.state.cursorCol] + currentLine[deleteTo:]
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Cursor movement
// ---------------------------------------------------------------------------

func (e *Editor) moveToLineStart() {
	e.lastAction = ""
	e.setCursorCol(0)
}

func (e *Editor) moveToLineEnd() {
	e.lastAction = ""
	currentLine := e.state.lines[e.state.cursorLine]
	e.setCursorCol(len(currentLine))
}

func (e *Editor) moveCursor(deltaLine, deltaCol int) {
	e.lastAction = ""
	vls := e.buildVisualLineMap(e.lastWidth)
	currentVL := e.findCurrentVisualLine(vls)

	if deltaLine != 0 {
		target := currentVL + deltaLine
		if target >= 0 && target < len(vls) {
			e.moveToVisualLine(vls, currentVL, target)
		}
	}

	if deltaCol != 0 {
		currentLine := e.state.lines[e.state.cursorLine]
		if deltaCol > 0 {
			if e.state.cursorCol < len(currentLine) {
				_, size := utf8.DecodeRuneInString(currentLine[e.state.cursorCol:])
				e.setCursorCol(e.state.cursorCol + size)
			} else if e.state.cursorLine < len(e.state.lines)-1 {
				e.state.cursorLine++
				e.setCursorCol(0)
			} else {
				// At end of last line - set preferredVisualCol
				currentVLData := vls[currentVL]
				col := e.state.cursorCol - currentVLData.startCol
				e.preferredVisualCol = &col
			}
		} else {
			if e.state.cursorCol > 0 {
				_, size := utf8.DecodeLastRuneInString(currentLine[:e.state.cursorCol])
				e.setCursorCol(e.state.cursorCol - size)
			} else if e.state.cursorLine > 0 {
				e.state.cursorLine--
				prevLine := e.state.lines[e.state.cursorLine]
				e.setCursorCol(len(prevLine))
			}
		}
	}
}

func (e *Editor) moveToVisualLine(vls []visualLine, currentVL, targetVL int) {
	cvl := vls[currentVL]
	tvl := vls[targetVL]

	currentVisualCol := e.state.cursorCol - cvl.startCol

	isLastSource := currentVL == len(vls)-1 || vls[currentVL+1].logicalLine != cvl.logicalLine
	sourceMaxVisualCol := cvl.length
	if !isLastSource {
		sourceMaxVisualCol = max(0, cvl.length-1)
	}

	isLastTarget := targetVL == len(vls)-1 || vls[targetVL+1].logicalLine != tvl.logicalLine
	targetMaxVisualCol := tvl.length
	if !isLastTarget {
		targetMaxVisualCol = max(0, tvl.length-1)
	}

	moveToCol := e.computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol)

	e.state.cursorLine = tvl.logicalLine
	targetCol := tvl.startCol + moveToCol
	logicalLine := e.state.lines[tvl.logicalLine]
	if targetCol > len(logicalLine) {
		targetCol = len(logicalLine)
	}
	e.state.cursorCol = targetCol
}

func (e *Editor) computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol int) int {
	hasPreferred := e.preferredVisualCol != nil
	cursorInMiddle := currentVisualCol < sourceMaxVisualCol
	targetTooShort := targetMaxVisualCol < currentVisualCol

	if !hasPreferred || cursorInMiddle {
		if targetTooShort {
			e.preferredVisualCol = &currentVisualCol
			return targetMaxVisualCol
		}
		e.preferredVisualCol = nil
		return currentVisualCol
	}

	targetCantFitPreferred := targetMaxVisualCol < *e.preferredVisualCol
	if targetTooShort || targetCantFitPreferred {
		return targetMaxVisualCol
	}

	result := *e.preferredVisualCol
	e.preferredVisualCol = nil
	return result
}

func (e *Editor) pageScroll(direction int) {
	e.lastAction = ""
	termRows := 24
	if e.tuiRef != nil {
		termRows = e.tuiRef.Terminal.Rows()
	}
	pageSize := max(5, int(math.Floor(float64(termRows)*0.3)))

	vls := e.buildVisualLineMap(e.lastWidth)
	currentVL := e.findCurrentVisualLine(vls)
	targetVL := currentVL + direction*pageSize
	if targetVL < 0 {
		targetVL = 0
	}
	if targetVL >= len(vls) {
		targetVL = len(vls) - 1
	}
	e.moveToVisualLine(vls, currentVL, targetVL)
}

func (e *Editor) moveWordBackwards() {
	e.lastAction = ""
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.state.cursorLine--
			prevLine := e.state.lines[e.state.cursorLine]
			e.setCursorCol(len(prevLine))
		}
		return
	}

	textBeforeCursor := currentLine[:e.state.cursorCol]
	newCol := e.state.cursorCol

	// Skip trailing whitespace
	for newCol > 0 {
		r, size := utf8.DecodeLastRuneInString(textBeforeCursor[:newCol])
		if !unicode.IsSpace(r) {
			break
		}
		newCol -= size
	}

	if newCol > 0 {
		r, _ := utf8.DecodeLastRuneInString(textBeforeCursor[:newCol])
		if tui.IsPunctuationChar(r) {
			for newCol > 0 {
				r, size := utf8.DecodeLastRuneInString(textBeforeCursor[:newCol])
				if !tui.IsPunctuationChar(r) {
					break
				}
				newCol -= size
			}
		} else {
			for newCol > 0 {
				r, size := utf8.DecodeLastRuneInString(textBeforeCursor[:newCol])
				if unicode.IsSpace(r) || tui.IsPunctuationChar(r) {
					break
				}
				newCol -= size
			}
		}
	}

	e.setCursorCol(newCol)
}

func (e *Editor) moveWordForwards() {
	e.lastAction = ""
	currentLine := e.state.lines[e.state.cursorLine]

	if e.state.cursorCol >= len(currentLine) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.state.cursorLine++
			e.setCursorCol(0)
		}
		return
	}

	textAfterCursor := currentLine[e.state.cursorCol:]
	offset := 0

	// Skip leading whitespace
	for offset < len(textAfterCursor) {
		r, size := utf8.DecodeRuneInString(textAfterCursor[offset:])
		if !unicode.IsSpace(r) {
			break
		}
		offset += size
	}

	if offset < len(textAfterCursor) {
		r, _ := utf8.DecodeRuneInString(textAfterCursor[offset:])
		if tui.IsPunctuationChar(r) {
			for offset < len(textAfterCursor) {
				r, size := utf8.DecodeRuneInString(textAfterCursor[offset:])
				if !tui.IsPunctuationChar(r) {
					break
				}
				offset += size
			}
		} else {
			for offset < len(textAfterCursor) {
				r, size := utf8.DecodeRuneInString(textAfterCursor[offset:])
				if unicode.IsSpace(r) || tui.IsPunctuationChar(r) {
					break
				}
				offset += size
			}
		}
	}

	e.setCursorCol(e.state.cursorCol + offset)
}

// ---------------------------------------------------------------------------
// Kill ring / yank
// ---------------------------------------------------------------------------

func (e *Editor) yank() {
	if e.killRing.Len() == 0 {
		return
	}
	e.pushUndoSnapshot()
	text := e.killRing.Peek()
	e.insertYankedText(text)
	e.lastAction = "yank"
}

func (e *Editor) yankPop() {
	if e.lastAction != "yank" || e.killRing.Len() <= 1 {
		return
	}
	e.pushUndoSnapshot()
	e.deleteYankedText()
	e.killRing.Rotate()
	text := e.killRing.Peek()
	e.insertYankedText(text)
	e.lastAction = "yank"
}

func (e *Editor) insertYankedText(text string) {
	e.historyIndex = -1
	lines := strings.Split(text, "\n")

	if len(lines) == 1 {
		currentLine := e.state.lines[e.state.cursorLine]
		before := currentLine[:e.state.cursorCol]
		after := currentLine[e.state.cursorCol:]
		e.state.lines[e.state.cursorLine] = before + text + after
		e.setCursorCol(e.state.cursorCol + len(text))
	} else {
		currentLine := e.state.lines[e.state.cursorLine]
		before := currentLine[:e.state.cursorCol]
		after := currentLine[e.state.cursorCol:]

		e.state.lines[e.state.cursorLine] = before + lines[0]

		for i := 1; i < len(lines)-1; i++ {
			insertAt := e.state.cursorLine + i
			newLines := make([]string, len(e.state.lines)+1)
			copy(newLines, e.state.lines[:insertAt])
			newLines[insertAt] = lines[i]
			copy(newLines[insertAt+1:], e.state.lines[insertAt:])
			e.state.lines = newLines
		}

		lastIdx := e.state.cursorLine + len(lines) - 1
		newLines := make([]string, len(e.state.lines)+1)
		copy(newLines, e.state.lines[:lastIdx])
		newLines[lastIdx] = lines[len(lines)-1] + after
		copy(newLines[lastIdx+1:], e.state.lines[lastIdx:])
		e.state.lines = newLines

		e.state.cursorLine = lastIdx
		e.setCursorCol(len(lines[len(lines)-1]))
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

func (e *Editor) deleteYankedText() {
	yankedText := e.killRing.Peek()
	if yankedText == "" {
		return
	}
	yankLines := strings.Split(yankedText, "\n")

	if len(yankLines) == 1 {
		currentLine := e.state.lines[e.state.cursorLine]
		deleteLen := len(yankedText)
		before := currentLine[:e.state.cursorCol-deleteLen]
		after := currentLine[e.state.cursorCol:]
		e.state.lines[e.state.cursorLine] = before + after
		e.setCursorCol(e.state.cursorCol - deleteLen)
	} else {
		startLine := e.state.cursorLine - (len(yankLines) - 1)
		startCol := len(e.state.lines[startLine]) - len(yankLines[0])
		afterCursor := e.state.lines[e.state.cursorLine][e.state.cursorCol:]
		beforeYank := e.state.lines[startLine][:startCol]

		// Remove lines from startLine to cursorLine, replace with merged
		newLines := make([]string, 0, len(e.state.lines)-len(yankLines)+1)
		newLines = append(newLines, e.state.lines[:startLine]...)
		newLines = append(newLines, beforeYank+afterCursor)
		newLines = append(newLines, e.state.lines[e.state.cursorLine+1:]...)
		e.state.lines = newLines

		e.state.cursorLine = startLine
		e.setCursorCol(startCol)
	}

	if e.OnChange != nil {
		e.OnChange(e.GetText())
	}
}

// ---------------------------------------------------------------------------
// Character jump
// ---------------------------------------------------------------------------

func (e *Editor) jumpToChar(ch, direction string) {
	e.lastAction = ""
	isForward := direction == "forward"

	if isForward {
		for lineIdx := e.state.cursorLine; lineIdx < len(e.state.lines); lineIdx++ {
			line := e.state.lines[lineIdx]
			searchFrom := 0
			if lineIdx == e.state.cursorLine {
				searchFrom = e.state.cursorCol + 1
			}
			idx := strings.Index(line[searchFrom:], ch)
			if idx != -1 {
				e.state.cursorLine = lineIdx
				e.setCursorCol(searchFrom + idx)
				return
			}
		}
	} else {
		for lineIdx := e.state.cursorLine; lineIdx >= 0; lineIdx-- {
			line := e.state.lines[lineIdx]
			searchEnd := len(line)
			if lineIdx == e.state.cursorLine {
				if e.state.cursorCol == 0 {
					continue
				}
				searchEnd = e.state.cursorCol
			}
			idx := strings.LastIndex(line[:searchEnd], ch)
			if idx != -1 {
				e.state.cursorLine = lineIdx
				e.setCursorCol(idx)
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Autocomplete
// ---------------------------------------------------------------------------

func (e *Editor) isSlashMenuAllowed() bool {
	return e.state.cursorLine == 0
}

func (e *Editor) isAtStartOfMessage() bool {
	if !e.isSlashMenuAllowed() {
		return false
	}
	currentLine := e.state.lines[e.state.cursorLine]
	beforeCursor := currentLine[:e.state.cursorCol]
	trimmed := strings.TrimSpace(beforeCursor)
	return trimmed == "" || trimmed == "/"
}

func (e *Editor) isInSlashCommandContext(textBeforeCursor string) bool {
	return e.isSlashMenuAllowed() && strings.HasPrefix(strings.TrimLeft(textBeforeCursor, " \t"), "/")
}

func (e *Editor) tryTriggerAutocomplete(explicitTab bool) {
	if e.autocompleteProvider == nil {
		return
	}

	if explicitTab {
		if fp, ok := e.autocompleteProvider.(ForceFileSuggestionsProvider); ok {
			if !fp.ShouldTriggerFileCompletion(e.state.lines, e.state.cursorLine, e.state.cursorCol) {
				return
			}
		}
	}

	suggestions := e.autocompleteProvider.GetSuggestions(e.state.lines, e.state.cursorLine, e.state.cursorCol)
	if suggestions != nil && len(suggestions.Items) > 0 {
		e.autocompletePrefix = suggestions.Prefix
		e.autocompleteList = NewSelectList(suggestions.Items, e.autocompleteMaxVis, e.theme.SelectList)
		e.autocompleteState = "regular"
	} else {
		e.cancelAutocomplete()
	}
}

func (e *Editor) handleTabCompletion() {
	if e.autocompleteProvider == nil {
		return
	}
	currentLine := e.state.lines[e.state.cursorLine]
	beforeCursor := currentLine[:e.state.cursorCol]

	if e.isInSlashCommandContext(beforeCursor) && !strings.Contains(strings.TrimLeft(beforeCursor, " \t"), " ") {
		e.tryTriggerAutocomplete(true)
	} else {
		e.forceFileAutocomplete(true)
	}
}

func (e *Editor) forceFileAutocomplete(explicitTab bool) {
	if e.autocompleteProvider == nil {
		return
	}

	fp, ok := e.autocompleteProvider.(ForceFileSuggestionsProvider)
	if !ok {
		e.tryTriggerAutocomplete(true)
		return
	}

	suggestions := fp.GetForceFileSuggestions(e.state.lines, e.state.cursorLine, e.state.cursorCol)
	if suggestions != nil && len(suggestions.Items) > 0 {
		// Single suggestion → apply immediately on tab
		if explicitTab && len(suggestions.Items) == 1 {
			item := suggestions.Items[0]
			e.pushUndoSnapshot()
			e.lastAction = ""
			result := e.autocompleteProvider.ApplyCompletion(
				e.state.lines, e.state.cursorLine, e.state.cursorCol,
				item, suggestions.Prefix,
			)
			e.state.lines = result.Lines
			e.state.cursorLine = result.CursorLine
			e.setCursorCol(result.CursorCol)
			if e.OnChange != nil {
				e.OnChange(e.GetText())
			}
			return
		}

		e.autocompletePrefix = suggestions.Prefix
		e.autocompleteList = NewSelectList(suggestions.Items, e.autocompleteMaxVis, e.theme.SelectList)
		e.autocompleteState = "force"
	} else {
		e.cancelAutocomplete()
	}
}

func (e *Editor) cancelAutocomplete() {
	e.autocompleteState = ""
	e.autocompleteList = nil
	e.autocompletePrefix = ""
}

func (e *Editor) updateAutocomplete() {
	if e.autocompleteState == "" || e.autocompleteProvider == nil {
		return
	}
	if e.autocompleteState == "force" {
		e.forceFileAutocomplete(false)
		return
	}
	suggestions := e.autocompleteProvider.GetSuggestions(e.state.lines, e.state.cursorLine, e.state.cursorCol)
	if suggestions != nil && len(suggestions.Items) > 0 {
		e.autocompletePrefix = suggestions.Prefix
		e.autocompleteList = NewSelectList(suggestions.Items, e.autocompleteMaxVis, e.theme.SelectList)
	} else {
		e.cancelAutocomplete()
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var atFileRefRe = regexp.MustCompile(`(?:^|[\s])@[^\s]*$`)

func matchAtFileRef(text string) bool {
	return atFileRefRe.MatchString(text)
}

func isAlnumDotDashUnderscore(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return len(s) > 0
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}


