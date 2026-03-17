// ANSI escape code parsing and tracking for terminal rendering.
// Split from utils.go.
package tui

import (
	"regexp"
	"strings"
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
	fgColor       string
	bgColor       string
}

// Process updates the tracker state based on an ANSI SGR code.
func (t *AnsiCodeTracker) Process(ansiCode string) {
	if len(ansiCode) < 3 || ansiCode[len(ansiCode)-1] != 'm' {
		return
	}

	inner := ansiCode[2 : len(ansiCode)-1]
	if inner == "" || inner == "0" {
		t.Reset()
		return
	}

	parts := strings.Split(inner, ";")
	i := 0
	for i < len(parts) {
		code := parseInt(parts[i])

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
