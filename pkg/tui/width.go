// Terminal width calculation, truncation, and column slicing.
// Split from utils.go.
package tui

import (
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

// graphemeWidth returns the terminal display width of a grapheme cluster.
func graphemeWidth(cluster string) int {
	if len(cluster) == 0 {
		return 0
	}

	r, _ := firstRune(cluster)

	if couldBeEmoji(r, cluster) {
		return 2
	}

	return runewidth.RuneWidth(r)
}

func firstRune(s string) (rune, int) {
	for i, r := range s {
		return r, i
	}
	return 0, 0
}

func couldBeEmoji(r rune, cluster string) bool {
	return (r >= 0x1f000 && r <= 0x1fbff) ||
		(r >= 0x2300 && r <= 0x23ff) ||
		(r >= 0x2600 && r <= 0x27bf) ||
		(r >= 0x2b50 && r <= 0x2b55) ||
		strings.Contains(cluster, "\uFE0F") ||
		len([]rune(cluster)) > 2
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
