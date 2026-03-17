// ANSI-aware text wrapping for terminal rendering.
// Split from utils.go.
package tui

import (
	"strings"

	"github.com/rivo/uniseg"
)

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
			end := i
			for end < len(word) {
				if _, l := ExtractAnsiCode(word, end); l > 0 {
					break
				}
				end++
			}
			textPortion := word[i:end]
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
