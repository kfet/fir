// Ported from: packages/coding-agent/src/utils/changelog.ts
// Upstream hash: 1caadb2e
package session

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// embeddedChangelogContent holds the CHANGELOG.md content baked in at build time.
// Set by cmd/fir/changelog_init.go via SetEmbeddedChangelog using //go:embed.
var embeddedChangelogContent string

// SetEmbeddedChangelog stores the changelog content embedded in the binary.
// Called once at init time from cmd/fir before any mode is started.
func SetEmbeddedChangelog(content string) {
	embeddedChangelogContent = content
}

// GetChangelogEntries returns changelog entries. It prefers content baked in at
// build time (via SetEmbeddedChangelog) and falls back to reading the file from
// DefaultAgentDir so that plain `go run` from the repo root still works.
func GetChangelogEntries() []ChangelogEntry {
	if embeddedChangelogContent != "" {
		return ParseChangelogContent(embeddedChangelogContent)
	}
	return ParseChangelog(filepath.Join(DefaultAgentDir(), "CHANGELOG.md"))
}

// ChangelogEntry represents a version entry in a CHANGELOG.md file.
type ChangelogEntry struct {
	Major   int
	Minor   int
	Patch   int
	Content string
}

// Version returns the version string (e.g. "1.2.3").
func (e ChangelogEntry) Version() string {
	return fmt.Sprintf("%d.%d.%d", e.Major, e.Minor, e.Patch)
}

var versionHeaderRe = regexp.MustCompile(`##\s+\[?(\d+)\.(\d+)\.(\d+)\]?`)

// ParseChangelog parses changelog entries from a CHANGELOG.md file.
func ParseChangelog(changelogPath string) []ChangelogEntry {
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		return nil
	}
	return ParseChangelogContent(string(data))
}

// ParseChangelogContent parses changelog entries from raw markdown content.
// Scans for ## lines with version numbers and collects content until the next ## or EOF.
func ParseChangelogContent(content string) []ChangelogEntry {
	lines := strings.Split(content, "\n")
	var entries []ChangelogEntry
	var currentLines []string
	var currentVersion *ChangelogEntry

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
					entries = appendEntry(entries, currentVersion, currentLines)

			// Try to parse version, or detect [Unreleased]
			m := versionHeaderRe.FindStringSubmatch(line)
			if m != nil {
				major, _ := strconv.Atoi(m[1])
				minor, _ := strconv.Atoi(m[2])
				patch, _ := strconv.Atoi(m[3])
				currentVersion = &ChangelogEntry{Major: major, Minor: minor, Patch: patch}
				currentLines = []string{line}
			} else if strings.Contains(strings.ToLower(line), "unreleased") {
				// Unreleased section: use max version so it sorts after all releases.
				currentVersion = &ChangelogEntry{Major: 999, Minor: 999, Patch: 999}
				currentLines = []string{line}
			} else {
				currentVersion = nil
				currentLines = nil
			}
		} else if currentVersion != nil {
			currentLines = append(currentLines, line)
		}
	}

	return appendEntry(entries, currentVersion, currentLines)
}

// appendEntry finalises an entry and appends it. An entry with only a header
// line and no body (typically an empty "## [Unreleased]") is dropped.
func appendEntry(entries []ChangelogEntry, e *ChangelogEntry, lines []string) []ChangelogEntry {
	if e == nil || len(lines) == 0 {
		return entries
	}
	if strings.TrimSpace(strings.Join(lines[1:], "\n")) == "" {
		return entries
	}
	e.Content = strings.TrimSpace(strings.Join(lines, "\n"))
	return append(entries, *e)
}

// DefaultChangelogLimit is how many entries /changelog shows without an argument.
const DefaultChangelogLimit = 5

// ChangelogUsage is the usage line for the /changelog command.
const ChangelogUsage = "Usage: /changelog [N|all] — last N releases (default 5), or all."

// SelectChangelogEntries applies the /changelog argument to entries (newest
// first). Empty arg means the last DefaultChangelogLimit entries; "all" means
// everything; a positive integer N means the last N. It returns the selected
// entries, a footer to show when the list was cut ("" otherwise), and an
// error for an invalid argument.
func SelectChangelogEntries(entries []ChangelogEntry, arg string) ([]ChangelogEntry, string, error) {
	arg = strings.TrimSpace(arg)
	limit := DefaultChangelogLimit
	switch {
	case arg == "":
	case strings.EqualFold(arg, "all"):
		return entries, "", nil
	default:
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("invalid argument %q. %s", arg, ChangelogUsage)
		}
		limit = n
	}
	if len(entries) <= limit {
		return entries, "", nil
	}
	footer := fmt.Sprintf("Showing %d of %d releases — /changelog all for full history.", limit, len(entries))
	return entries[:limit], footer, nil
}

// CompareVersions compares two changelog entries by version.
// Returns -1 if a < b, 0 if equal, 1 if a > b.
func CompareVersions(a, b ChangelogEntry) int {
	if a.Major != b.Major {
		return cmp.Compare(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return cmp.Compare(a.Minor, b.Minor)
	}
	return cmp.Compare(a.Patch, b.Patch)
}

// GetNewEntries returns entries newer than the given version string (e.g. "1.2.3").
func GetNewEntries(entries []ChangelogEntry, lastVersion string) []ChangelogEntry {
	parts := strings.Split(lastVersion, ".")
	last := ChangelogEntry{}
	if len(parts) >= 1 {
		last.Major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) >= 2 {
		last.Minor, _ = strconv.Atoi(parts[1])
	}
	if len(parts) >= 3 {
		last.Patch, _ = strconv.Atoi(parts[2])
	}

	var result []ChangelogEntry
	for _, entry := range entries {
		if CompareVersions(entry, last) > 0 {
			result = append(result, entry)
		}
	}
	return result
}
