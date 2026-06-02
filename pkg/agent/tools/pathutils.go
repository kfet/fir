// Ported from: packages/coding-agent/src/core/tools/path-utils.ts
// Upstream hash: 1caadb2e
package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// unicodeSpaces matches various Unicode space characters that users might paste.
var unicodeSpaces = regexp.MustCompile("[\u00A0\u2000-\u200A\u202F\u205F\u3000]")

// narrowNoBreakSpace is used in macOS screenshot filenames before AM/PM.
const narrowNoBreakSpace = "\u202F"

// normalizeUnicodeSpaces replaces various Unicode spaces with regular ASCII space.
func normalizeUnicodeSpaces(s string) string {
	return unicodeSpaces.ReplaceAllString(s, " ")
}

// tryMacOSScreenshotPath replaces space before AM/PM with narrow no-break space
// (macOS screenshot naming convention).
var amPmPattern = regexp.MustCompile(` (AM|PM)\.`)

func tryMacOSScreenshotPath(filePath string) string {
	return amPmPattern.ReplaceAllString(filePath, narrowNoBreakSpace+"$1.")
}

// tryNFDVariant converts to NFD (decomposed) form.
// macOS stores filenames in NFD form, so converting user input to NFD may match.
func tryNFDVariant(filePath string) string {
	return norm.NFD.String(filePath)
}

// tryCurlyQuoteVariant replaces ASCII apostrophe with right single quotation mark.
// macOS uses U+2019 in names like "Capture d'écran".
func tryCurlyQuoteVariant(filePath string) string {
	return strings.ReplaceAll(filePath, "'", "\u2019")
}

// fileExists checks if a file exists.
func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// normalizeAtPrefix strips a leading '@' from a path.
func normalizeAtPrefix(filePath string) string {
	if strings.HasPrefix(filePath, "@") {
		return filePath[1:]
	}
	return filePath
}

// ExpandPath expands ~ to the user's home directory and normalizes Unicode spaces.
func ExpandPath(filePath string) string {
	normalized := normalizeUnicodeSpaces(normalizeAtPrefix(filePath))
	if normalized == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(normalized, "~/") {
		home, _ := os.UserHomeDir()
		return home + normalized[1:]
	}
	return normalized
}

// ResolveToCwd resolves a path relative to the given cwd.
// Handles ~ expansion and absolute paths.
func ResolveToCwd(filePath string, cwd string) string {
	expanded := ExpandPath(filePath)
	if filepath.IsAbs(expanded) {
		return expanded
	}
	return filepath.Join(cwd, expanded)
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ResolveReadPath resolves a path for reading, trying macOS filename variants
// if the literal path doesn't exist.
func ResolveReadPath(filePath string, cwd string) string {
	resolved := ResolveToCwd(filePath, cwd)

	if fileExists(resolved) {
		return resolved
	}

	// Try macOS AM/PM variant (narrow no-break space before AM/PM)
	amPmVariant := tryMacOSScreenshotPath(resolved)
	if amPmVariant != resolved && fileExists(amPmVariant) {
		return amPmVariant
	}

	// Try NFD variant (macOS stores filenames in NFD form)
	nfdVariant := tryNFDVariant(resolved)
	if nfdVariant != resolved && fileExists(nfdVariant) {
		return nfdVariant
	}

	// Try curly quote variant (macOS uses U+2019 in screenshot names)
	curlyVariant := tryCurlyQuoteVariant(resolved)
	if curlyVariant != resolved && fileExists(curlyVariant) {
		return curlyVariant
	}

	// Try combined NFD + curly quote (for French macOS screenshots)
	nfdCurlyVariant := tryCurlyQuoteVariant(nfdVariant)
	if nfdCurlyVariant != resolved && fileExists(nfdCurlyVariant) {
		return nfdCurlyVariant
	}

	return resolved
}

// IsHidden returns true if a path component starts with a dot.
func IsHidden(name string) bool {
	for _, r := range name {
		if r == '.' {
			return true
		}
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return false
}
