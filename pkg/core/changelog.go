// Ported from: packages/coding-agent/src/utils/changelog.ts
// Upstream hash: 1caadb2e
package core

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

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
			// Save previous entry
			if currentVersion != nil && len(currentLines) > 0 {
				currentVersion.Content = strings.TrimSpace(strings.Join(currentLines, "\n"))
				entries = append(entries, *currentVersion)
			}

			// Try to parse version
			m := versionHeaderRe.FindStringSubmatch(line)
			if m != nil {
				major, _ := strconv.Atoi(m[1])
				minor, _ := strconv.Atoi(m[2])
				patch, _ := strconv.Atoi(m[3])
				currentVersion = &ChangelogEntry{Major: major, Minor: minor, Patch: patch}
				currentLines = []string{line}
			} else {
				currentVersion = nil
				currentLines = nil
			}
		} else if currentVersion != nil {
			currentLines = append(currentLines, line)
		}
	}

	// Save last entry
	if currentVersion != nil && len(currentLines) > 0 {
		currentVersion.Content = strings.TrimSpace(strings.Join(currentLines, "\n"))
		entries = append(entries, *currentVersion)
	}

	return entries
}

// CompareVersions compares two changelog entries by version.
// Returns -1 if a < b, 0 if equal, 1 if a > b.
func CompareVersions(a, b ChangelogEntry) int {
	if a.Major != b.Major {
		return cmpInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return cmpInt(a.Minor, b.Minor)
	}
	return cmpInt(a.Patch, b.Patch)
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
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
