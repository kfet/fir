// Ported from: packages/tui/src/utils.ts
// Upstream hash: 1caadb2e
package tui

import (
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// VisibleWidth calculates the terminal width of a string in columns.
// It strips ANSI escape codes and handles wide characters and grapheme clusters.
func VisibleWidth(s string) int {
	if len(s) == 0 {
		return 0
	}

	// Fast path: pure ASCII printable (no ANSI escapes)
	isPureASCII := true
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			isPureASCII = false
			break
		}
	}
	if isPureASCII {
		return len(s)
	}

	// Check cache
	widthCacheMu.RLock()
	if w, ok := widthCache[s]; ok {
		widthCacheMu.RUnlock()
		return w
	}
	widthCacheMu.RUnlock()

	// Normalize
	clean := s
	if strings.Contains(clean, "\t") {
		clean = strings.ReplaceAll(clean, "\t", "   ")
	}
	if strings.Contains(clean, "\x1b") {
		clean = StripAnsi(clean)
	}

	// Calculate width using grapheme clusters
	width := 0
	state := -1
	for len(clean) > 0 {
		cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(clean, state)
		width += graphemeWidth(cluster)
		clean = rest
		state = newState
	}

	// Cache result
	widthCacheMu.Lock()
	if len(widthCache) >= widthCacheSize {
		// Evict one entry
		for k := range widthCache {
			delete(widthCache, k)
			break
		}
	}
	widthCache[s] = width
	widthCacheMu.Unlock()

	return width
}

const widthCacheSize = 512

var (
	widthCache   = make(map[string]int, widthCacheSize)
	widthCacheMu sync.RWMutex
)

// ANSI stripping regex patterns
var (
	ansiSGRRe  = regexp.MustCompile(`\x1b\[[0-9;]*[mGKHJ]`)
	ansiOSC8Re = regexp.MustCompile(`\x1b\]8;;[^\x07]*\x07`)
	ansiAPCRe  = regexp.MustCompile(`\x1b_[^\x07\x1b]*(?:\x07|\x1b\\)`)
)

func StripAnsi(s string) string {
	s = ansiSGRRe.ReplaceAllString(s, "")
	s = ansiOSC8Re.ReplaceAllString(s, "")
	s = ansiAPCRe.ReplaceAllString(s, "")
	return s
}

// graphemeWidth returns the terminal display width of a grapheme cluster.
func graphemeWidth(cluster string) int {
	if len(cluster) == 0 {
		return 0
	}

	// Check for emoji (multi-codepoint sequences, variation selectors)
	r, _ := firstRune(cluster)

	// Check for emoji ranges
	if couldBeEmoji(r, cluster) {
		// Emojis are typically 2 cells wide
		return 2
	}

	// Use runewidth for the first rune (handles East Asian width)
	return runewidth.RuneWidth(r)
}

func firstRune(s string) (rune, int) {
	for i, r := range s {
		return r, i
	}
	return 0, 0
}

func couldBeEmoji(r rune, cluster string) bool {
	return (r >= 0x1f000 && r <= 0x1fbff) || // Emoji and Pictograph
		(r >= 0x2300 && r <= 0x23ff) || // Misc technical
		(r >= 0x2600 && r <= 0x27bf) || // Misc symbols, dingbats
		(r >= 0x2b50 && r <= 0x2b55) || // Specific stars/circles
		strings.Contains(cluster, "\uFE0F") || // VS16
		len([]rune(cluster)) > 2 // Multi-codepoint (ZWJ, skin tones)
}

// ExtractAnsiCode extracts an ANSI escape sequence at the given byte position.
// Returns the code string and its byte length, or ("", 0) if none found.
func ExtractAnsiCode(s string, pos int) (code string, length int) {
	if pos >= len(s) || s[pos] != '\x1b' {
		return "", 0
	}
	if pos+1 >= len(s) {
		return "", 0
	}

	next := s[pos+1]

	// CSI: ESC [ ... m/G/K/H/J
	if next == '[' {
		j := pos + 2
		for j < len(s) {
			c := s[j]
			if c == 'm' || c == 'G' || c == 'K' || c == 'H' || c == 'J' {
				return s[pos : j+1], j + 1 - pos
			}
			if (c >= '0' && c <= '9') || c == ';' {
				j++
				continue
			}
			return "", 0
		}
		return "", 0
	}

	// OSC: ESC ] ... BEL or ESC ] ... ST
	if next == ']' {
		j := pos + 2
		for j < len(s) {
			if s[j] == '\x07' {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}

	// APC: ESC _ ... BEL or ESC _ ... ST
	if next == '_' {
		j := pos + 2
		for j < len(s) {
			if s[j] == '\x07' {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}

	return "", 0
}

// AnsiCodeTracker tracks active ANSI SGR codes to preserve styling across line breaks.
type AnsiCodeTracker struct {
	bold          bool
	dim           bool
	italic        bool
	underline     bool
	blink         bool
	inverse       bool
	hidden        bool
	strikethrough bool
	fgColor       string // e.g. "31" or "38;5;240"
	bgColor       string // e.g. "41" or "48;5;240"
}

// Process updates the tracker state based on an ANSI SGR code.
func (t *AnsiCodeTracker) Process(ansiCode string) {
	if len(ansiCode) < 3 || ansiCode[len(ansiCode)-1] != 'm' {
		return
	}

	// Extract parameters between \x1b[ and m
	inner := ansiCode[2 : len(ansiCode)-1]
	if inner == "" || inner == "0" {
		t.Reset()
		return
	}

	parts := strings.Split(inner, ";")
	i := 0
	for i < len(parts) {
		code := parseInt(parts[i])

		// 256-color and RGB
		if code == 38 || code == 48 {
			if i+2 < len(parts) && parts[i+1] == "5" {
				colorCode := parts[i] + ";" + parts[i+1] + ";" + parts[i+2]
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				i += 3
				continue
			}
			if i+4 < len(parts) && parts[i+1] == "2" {
				colorCode := strings.Join(parts[i:i+5], ";")
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				i += 5
				continue
			}
		}

		switch code {
		case 0:
			t.Reset()
		case 1:
			t.bold = true
		case 2:
			t.dim = true
		case 3:
			t.italic = true
		case 4:
			t.underline = true
		case 5:
			t.blink = true
		case 7:
			t.inverse = true
		case 8:
			t.hidden = true
		case 9:
			t.strikethrough = true
		case 21:
			t.bold = false
		case 22:
			t.bold = false
			t.dim = false
		case 23:
			t.italic = false
		case 24:
			t.underline = false
		case 25:
			t.blink = false
		case 27:
			t.inverse = false
		case 28:
			t.hidden = false
		case 29:
			t.strikethrough = false
		case 39:
			t.fgColor = ""
		case 49:
			t.bgColor = ""
		default:
			if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
				t.fgColor = parts[i]
			} else if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				t.bgColor = parts[i]
			}
		}
		i++
	}
}

// Reset clears all tracked state.
func (t *AnsiCodeTracker) Reset() {
	*t = AnsiCodeTracker{}
}

// Clear is an alias for Reset.
func (t *AnsiCodeTracker) Clear() {
	t.Reset()
}

// GetActiveCodes returns an ANSI escape sequence that restores all active attributes.
func (t *AnsiCodeTracker) GetActiveCodes() string {
	var codes []string
	if t.bold {
		codes = append(codes, "1")
	}
	if t.dim {
		codes = append(codes, "2")
	}
	if t.italic {
		codes = append(codes, "3")
	}
	if t.underline {
		codes = append(codes, "4")
	}
	if t.blink {
		codes = append(codes, "5")
	}
	if t.inverse {
		codes = append(codes, "7")
	}
	if t.hidden {
		codes = append(codes, "8")
	}
	if t.strikethrough {
		codes = append(codes, "9")
	}
	if t.fgColor != "" {
		codes = append(codes, t.fgColor)
	}
	if t.bgColor != "" {
		codes = append(codes, t.bgColor)
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// HasActiveCodes returns true if any style attributes are active.
func (t *AnsiCodeTracker) HasActiveCodes() bool {
	return t.bold || t.dim || t.italic || t.underline || t.blink ||
		t.inverse || t.hidden || t.strikethrough ||
		t.fgColor != "" || t.bgColor != ""
}

// GetLineEndReset returns reset codes for attributes that bleed into padding (underline).
func (t *AnsiCodeTracker) GetLineEndReset() string {
	if t.underline {
		return "\x1b[24m"
	}
	return ""
}

// UpdateTrackerFromText scans text for ANSI codes and updates the tracker.
func UpdateTrackerFromText(text string, tracker *AnsiCodeTracker) {
	i := 0
	for i < len(text) {
		code, length := ExtractAnsiCode(text, i)
		if length > 0 {
			tracker.Process(code)
			i += length
		} else {
			i++
		}
	}
}

// splitIntoTokensWithAnsi splits text into words while keeping ANSI codes attached.
func splitIntoTokensWithAnsi(text string) []string {
	var tokens []string
	var current strings.Builder
	var pendingAnsi strings.Builder
	inWhitespace := false
	i := 0

	for i < len(text) {
		code, length := ExtractAnsiCode(text, i)
		if length > 0 {
			pendingAnsi.WriteString(code)
			i += length
			continue
		}

		ch := text[i]
		charIsSpace := ch == ' '

		if charIsSpace != inWhitespace && current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}

		if pendingAnsi.Len() > 0 {
			current.WriteString(pendingAnsi.String())
			pendingAnsi.Reset()
		}

		inWhitespace = charIsSpace
		current.WriteByte(ch)
		i++
	}

	if pendingAnsi.Len() > 0 {
		current.WriteString(pendingAnsi.String())
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// WrapTextWithAnsi wraps text preserving ANSI codes across line breaks.
// Returns lines where each line is <= width visible chars.
// Only does word wrapping — NO padding, NO background colors.
func WrapTextWithAnsi(text string, width int) []string {
	if text == "" {
		return []string{""}
	}

	inputLines := strings.Split(text, "\n")
	var result []string
	var tracker AnsiCodeTracker

	for _, inputLine := range inputLines {
		prefix := ""
		if len(result) > 0 {
			prefix = tracker.GetActiveCodes()
		}
		result = append(result, wrapSingleLine(prefix+inputLine, width)...)
		UpdateTrackerFromText(inputLine, &tracker)
	}

	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}

	if VisibleWidth(line) <= width {
		return []string{line}
	}

	var wrapped []string
	var tracker AnsiCodeTracker
	tokens := splitIntoTokensWithAnsi(line)

	var currentLine strings.Builder
	currentVisibleLength := 0

	for _, token := range tokens {
		tokenVisibleLength := VisibleWidth(token)
		isWhitespace := strings.TrimSpace(token) == ""

		// Token too long — break character by character
		if tokenVisibleLength > width && !isWhitespace {
			if currentLine.Len() > 0 {
				lineEndReset := tracker.GetLineEndReset()
				if lineEndReset != "" {
					currentLine.WriteString(lineEndReset)
				}
				wrapped = append(wrapped, currentLine.String())
				currentLine.Reset()
				currentVisibleLength = 0
			}

			broken := breakLongWord(token, width, &tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			currentLine.WriteString(broken[len(broken)-1])
			currentVisibleLength = VisibleWidth(broken[len(broken)-1])
			continue
		}

		totalNeeded := currentVisibleLength + tokenVisibleLength

		if totalNeeded > width && currentVisibleLength > 0 {
			lineToWrap := strings.TrimRight(currentLine.String(), " ")
			lineEndReset := tracker.GetLineEndReset()
			if lineEndReset != "" {
				lineToWrap += lineEndReset
			}
			wrapped = append(wrapped, lineToWrap)
			currentLine.Reset()
			if isWhitespace {
				currentLine.WriteString(tracker.GetActiveCodes())
				currentVisibleLength = 0
			} else {
				currentLine.WriteString(tracker.GetActiveCodes())
				currentLine.WriteString(token)
				currentVisibleLength = tokenVisibleLength
			}
		} else {
			currentLine.WriteString(token)
			currentVisibleLength += tokenVisibleLength
		}

		UpdateTrackerFromText(token, &tracker)
	}

	if currentLine.Len() > 0 {
		wrapped = append(wrapped, currentLine.String())
	}

	if len(wrapped) == 0 {
		return []string{""}
	}

	// Trim trailing whitespace from each line
	for i := range wrapped {
		wrapped[i] = strings.TrimRight(wrapped[i], " ")
	}

	return wrapped
}

func breakLongWord(word string, width int, tracker *AnsiCodeTracker) []string {
	var lines []string
	var currentLine strings.Builder
	currentLine.WriteString(tracker.GetActiveCodes())
	currentWidth := 0

	// Parse into segments (ansi codes + grapheme clusters)
	type segment struct {
		isAnsi bool
		value  string
	}
	var segments []segment

	i := 0
	for i < len(word) {
		code, length := ExtractAnsiCode(word, i)
		if length > 0 {
			segments = append(segments, segment{isAnsi: true, value: code})
			i += length
		} else {
			// Find end of non-ANSI portion
			end := i
			for end < len(word) {
				if _, l := ExtractAnsiCode(word, end); l > 0 {
					break
				}
				end++
			}
			textPortion := word[i:end]
			// Segment into graphemes
			state := -1
			for len(textPortion) > 0 {
				cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(textPortion, state)
				segments = append(segments, segment{isAnsi: false, value: cluster})
				textPortion = rest
				state = newState
			}
			i = end
		}
	}

	for _, seg := range segments {
		if seg.isAnsi {
			currentLine.WriteString(seg.value)
			tracker.Process(seg.value)
			continue
		}

		gw := graphemeWidth(seg.value)
		if gw == 0 {
			continue
		}

		if currentWidth+gw > width {
			lineEndReset := tracker.GetLineEndReset()
			if lineEndReset != "" {
				currentLine.WriteString(lineEndReset)
			}
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(tracker.GetActiveCodes())
			currentWidth = 0
		}

		currentLine.WriteString(seg.value)
		currentWidth += gw
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// ApplyBackgroundToLine applies a background color function to a line, padding to full width.
func ApplyBackgroundToLine(line string, width int, bgFn func(string) string) string {
	visibleLen := VisibleWidth(line)
	paddingNeeded := width - visibleLen
	if paddingNeeded < 0 {
		paddingNeeded = 0
	}
	return bgFn(line + strings.Repeat(" ", paddingNeeded))
}

// TruncateToWidth truncates text to fit within maxWidth visible columns, adding ellipsis if needed.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if ellipsis == "" {
		ellipsis = "..."
	}

	textWidth := VisibleWidth(text)
	if textWidth <= maxWidth {
		if pad {
			return text + strings.Repeat(" ", maxWidth-textWidth)
		}
		return text
	}

	ellipsisWidth := VisibleWidth(ellipsis)
	targetWidth := maxWidth - ellipsisWidth
	if targetWidth <= 0 {
		return ellipsis[:maxWidth]
	}

	// Build truncated string from grapheme segments, skipping ANSI codes
	type seg struct {
		isAnsi bool
		value  string
	}
	var segments []seg

	i := 0
	for i < len(text) {
		code, length := ExtractAnsiCode(text, i)
		if length > 0 {
			segments = append(segments, seg{isAnsi: true, value: code})
			i += length
		} else {
			end := i
			for end < len(text) {
				if _, l := ExtractAnsiCode(text, end); l > 0 {
					break
				}
				end++
			}
			textPortion := text[i:end]
			state := -1
			for len(textPortion) > 0 {
				cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(textPortion, state)
				segments = append(segments, seg{isAnsi: false, value: cluster})
				textPortion = rest
				state = newState
			}
			i = end
		}
	}

	var result strings.Builder
	currentWidth := 0
	for _, s := range segments {
		if s.isAnsi {
			result.WriteString(s.value)
			continue
		}
		gw := graphemeWidth(s.value)
		if currentWidth+gw > targetWidth {
			break
		}
		result.WriteString(s.value)
		currentWidth += gw
	}

	truncated := result.String() + "\x1b[0m" + ellipsis
	if pad {
		tw := VisibleWidth(truncated)
		return truncated + strings.Repeat(" ", max(0, maxWidth-tw))
	}
	return truncated
}

// SliceByColumn extracts visible columns [startCol, startCol+length) from a line.
func SliceByColumn(line string, startCol, length int, strict bool) string {
	result, _ := SliceWithWidth(line, startCol, length, strict)
	return result
}

// SliceWithWidth extracts visible columns and returns both text and actual visible width.
func SliceWithWidth(line string, startCol, length int, strict bool) (string, int) {
	if length <= 0 {
		return "", 0
	}

	endCol := startCol + length
	var result strings.Builder
	resultWidth := 0
	currentCol := 0
	var pendingAnsi strings.Builder
	i := 0

	for i < len(line) {
		code, clen := ExtractAnsiCode(line, i)
		if clen > 0 {
			if currentCol >= startCol && currentCol < endCol {
				result.WriteString(code)
			} else if currentCol < startCol {
				pendingAnsi.WriteString(code)
			}
			i += clen
			continue
		}

		// Find end of non-ANSI text
		textEnd := i
		for textEnd < len(line) {
			if _, l := ExtractAnsiCode(line, textEnd); l > 0 {
				break
			}
			textEnd++
		}

		textPortion := line[i:textEnd]
		state := -1
		for len(textPortion) > 0 {
			cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(textPortion, state)
			w := graphemeWidth(cluster)

			inRange := currentCol >= startCol && currentCol < endCol
			fits := !strict || currentCol+w <= endCol

			if inRange && fits {
				if pendingAnsi.Len() > 0 {
					result.WriteString(pendingAnsi.String())
					pendingAnsi.Reset()
				}
				result.WriteString(cluster)
				resultWidth += w
			}

			currentCol += w
			textPortion = rest
			state = newState

			if currentCol >= endCol {
				break
			}
		}

		i = textEnd
		if currentCol >= endCol {
			break
		}
	}

	return result.String(), resultWidth
}

// ExtractSegmentsResult holds before/after segments for overlay compositing.
type ExtractSegmentsResult struct {
	Before      string
	BeforeWidth int
	After       string
	AfterWidth  int
}

// ExtractSegments extracts "before" and "after" segments from a line.
// Used for overlay compositing where we need content before and after the overlay region.
func ExtractSegments(line string, beforeEnd, afterStart, afterLen int, strictAfter bool) ExtractSegmentsResult {
	var before, after strings.Builder
	beforeWidth := 0
	afterWidth := 0
	currentCol := 0
	i := 0
	var pendingAnsiBefore strings.Builder
	afterStarted := false
	afterEnd := afterStart + afterLen
	var tracker AnsiCodeTracker

	for i < len(line) {
		code, clen := ExtractAnsiCode(line, i)
		if clen > 0 {
			tracker.Process(code)
			if currentCol < beforeEnd {
				pendingAnsiBefore.WriteString(code)
			} else if currentCol >= afterStart && currentCol < afterEnd && afterStarted {
				after.WriteString(code)
			}
			i += clen
			continue
		}

		textEnd := i
		for textEnd < len(line) {
			if _, l := ExtractAnsiCode(line, textEnd); l > 0 {
				break
			}
			textEnd++
		}

		textPortion := line[i:textEnd]
		state := -1
		for len(textPortion) > 0 {
			cluster, rest, _, newState := uniseg.FirstGraphemeClusterInString(textPortion, state)
			w := graphemeWidth(cluster)

			if currentCol < beforeEnd {
				if pendingAnsiBefore.Len() > 0 {
					before.WriteString(pendingAnsiBefore.String())
					pendingAnsiBefore.Reset()
				}
				before.WriteString(cluster)
				beforeWidth += w
			} else if currentCol >= afterStart && currentCol < afterEnd {
				fits := !strictAfter || currentCol+w <= afterEnd
				if fits {
					if !afterStarted {
						after.WriteString(tracker.GetActiveCodes())
						afterStarted = true
					}
					after.WriteString(cluster)
					afterWidth += w
				}
			}

			currentCol += w
			textPortion = rest
			state = newState

			doneCol := afterEnd
			if afterLen <= 0 {
				doneCol = beforeEnd
			}
			if currentCol >= doneCol {
				break
			}
		}
		i = textEnd
		doneCol := afterEnd
		if afterLen <= 0 {
			doneCol = beforeEnd
		}
		if currentCol >= doneCol {
			break
		}
	}

	return ExtractSegmentsResult{
		Before:      before.String(),
		BeforeWidth: beforeWidth,
		After:       after.String(),
		AfterWidth:  afterWidth,
	}
}

// SliceWithWidthResult holds text and its visible width.
type SliceWithWidthResult struct {
	Text  string
	Width int
}

// IsWhitespaceChar returns true if the rune is whitespace.
func IsWhitespaceChar(r rune) bool {
	return unicode.IsSpace(r)
}

// IsPunctuationChar returns true if the rune is a punctuation character.
func IsPunctuationChar(r rune) bool {
	return strings.ContainsRune(`(){}[]<>.,;:'"!?+-=*/\|&%^$#@~`+"`", r)
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
