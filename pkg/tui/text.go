// Character classification and text helpers.
package tui

import (
	"strings"
	"unicode"
)

// IsWhitespaceChar returns true if the rune is whitespace.
func IsWhitespaceChar(r rune) bool {
	return unicode.IsSpace(r)
}

// IsPunctuationChar returns true if the rune is a punctuation character.
func IsPunctuationChar(r rune) bool {
	return strings.ContainsRune(`(){}[]<>.,;:'"!?+-=*/\|&%^$#@~`+"`", r)
}

// isPureASCIIPrintable reports whether s contains only ASCII printable bytes (0x20–0x7e).
func isPureASCIIPrintable(s string) bool {
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
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
