// Terminal width calculation, truncation, and column slicing.
// Split from utils.go.
package tui

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rivo/uniseg"
)

// VisibleWidth calculates the terminal width of a string in columns.
// It strips ANSI escape codes and handles wide characters and grapheme clusters.
func VisibleWidth(s string) int {
	if len(s) == 0 {
		return 0
	}

	// Fast path: pure ASCII printable (no ANSI escapes)
	if isPureASCIIPrintable(s) {
		return len(s)
	}

	// Check cache
	if w, ok := widthCache.Load(s); ok {
		return w.(int)
	}

	// Normalize
	clean := strings.ReplaceAll(s, "\t", "   ")
	if strings.Contains(clean, "\x1b") {
		clean = StripAnsi(clean)
	}

	// Calculate width — uniseg.StringWidth handles grapheme clusters and emoji.
	width := uniseg.StringWidth(clean)

	// Cache result (evict all if too large, since sync.Map has no Len)
	widthCacheN.Add(1)
	if widthCacheN.Load() > widthCacheSize {
		widthCache.Clear()
		widthCacheN.Store(1)
	}
	widthCache.Store(s, width)

	return width
}

const widthCacheSize = 512

var (
	widthCache  sync.Map
	widthCacheN atomic.Int64
)

// graphemeWidth returns the terminal display width of a grapheme cluster.
// Uses uniseg which embeds Unicode Emoji_Presentation property tables,
// so emoji vs text-presentation is determined from spec data, not heuristics.
func graphemeWidth(cluster string) int {
	return uniseg.StringWidth(cluster)
}

// ApplyBackgroundToLine applies a background color function to a line, padding to full width.
func ApplyBackgroundToLine(line string, width int, bgFn func(string) string) string {
	visibleLen := VisibleWidth(line)
	paddingNeeded := max(width-visibleLen, 0)
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

	var result strings.Builder
	currentWidth := 0
	i := 0
	for i < len(text) {
		code, length := ExtractAnsiCode(text, i)
		if length > 0 {
			result.WriteString(code)
			i += length
			continue
		}

		// Find end of non-ANSI text
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
			gw := graphemeWidth(cluster)
			if currentWidth+gw > targetWidth {
				goto done
			}
			result.WriteString(cluster)
			currentWidth += gw
			textPortion = rest
			state = newState
		}
		i = end
	}
done:

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
