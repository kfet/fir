package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChangelog_FileNotFound(t *testing.T) {
	entries := ParseChangelog("/nonexistent/path/CHANGELOG.md")
	if entries != nil {
		t.Error("expected nil for nonexistent file")
	}
}

func TestParseChangelog_BasicEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")

	content := `# Changelog

## 2.1.0

- Added feature X
- Fixed bug Y

## 2.0.1

- Patch fix

## 2.0.0

- Major release
- Breaking changes
`
	os.WriteFile(path, []byte(content), 0644)

	entries := ParseChangelog(path)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Major != 2 || entries[0].Minor != 1 || entries[0].Patch != 0 {
		t.Errorf("entry 0 version = %s, want 2.1.0", entries[0].Version())
	}
	if entries[1].Major != 2 || entries[1].Minor != 0 || entries[1].Patch != 1 {
		t.Errorf("entry 1 version = %s, want 2.0.1", entries[1].Version())
	}
	if entries[2].Major != 2 || entries[2].Minor != 0 || entries[2].Patch != 0 {
		t.Errorf("entry 2 version = %s, want 2.0.0", entries[2].Version())
	}
}

func TestParseChangelog_BracketedVersions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")

	content := `## [1.2.3] - 2024-01-01

- Some change

## [1.2.2]

- Another change
`
	os.WriteFile(path, []byte(content), 0644)

	entries := ParseChangelog(path)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Version() != "1.2.3" {
		t.Errorf("expected 1.2.3, got %s", entries[0].Version())
	}
}

func TestParseChangelog_EntryContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CHANGELOG.md")

	content := `## 1.0.0

- Feature A
- Feature B

Some description.
`
	os.WriteFile(path, []byte(content), 0644)

	entries := ParseChangelog(path)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Content == "" {
		t.Error("expected non-empty content")
	}
	// Content should include the header and the body
	if entries[0].Content[:6] != "## 1.0" {
		t.Errorf("content should start with header, got: %q", entries[0].Content[:20])
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b ChangelogEntry
		want int
	}{
		{ChangelogEntry{1, 0, 0, ""}, ChangelogEntry{1, 0, 0, ""}, 0},
		{ChangelogEntry{2, 0, 0, ""}, ChangelogEntry{1, 0, 0, ""}, 1},
		{ChangelogEntry{1, 0, 0, ""}, ChangelogEntry{2, 0, 0, ""}, -1},
		{ChangelogEntry{1, 2, 0, ""}, ChangelogEntry{1, 1, 0, ""}, 1},
		{ChangelogEntry{1, 1, 0, ""}, ChangelogEntry{1, 2, 0, ""}, -1},
		{ChangelogEntry{1, 1, 3, ""}, ChangelogEntry{1, 1, 2, ""}, 1},
		{ChangelogEntry{1, 1, 2, ""}, ChangelogEntry{1, 1, 3, ""}, -1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d",
				tt.a.Version(), tt.b.Version(), got, tt.want)
		}
	}
}

func TestGetNewEntries(t *testing.T) {
	entries := []ChangelogEntry{
		{2, 1, 0, "new"},
		{2, 0, 1, "patch"},
		{2, 0, 0, "major"},
		{1, 9, 0, "old"},
	}

	newer := GetNewEntries(entries, "2.0.0")
	if len(newer) != 2 {
		t.Fatalf("expected 2 new entries, got %d", len(newer))
	}
	if newer[0].Version() != "2.1.0" {
		t.Errorf("expected 2.1.0, got %s", newer[0].Version())
	}
	if newer[1].Version() != "2.0.1" {
		t.Errorf("expected 2.0.1, got %s", newer[1].Version())
	}
}

func TestGetNewEntries_EmptyVersion(t *testing.T) {
	entries := []ChangelogEntry{
		{1, 0, 0, "first"},
	}

	newer := GetNewEntries(entries, "")
	if len(newer) != 1 {
		t.Fatalf("expected 1 new entry for empty version, got %d", len(newer))
	}
}

func TestChangelogEntry_Version(t *testing.T) {
	e := ChangelogEntry{Major: 1, Minor: 2, Patch: 3}
	if e.Version() != "1.2.3" {
		t.Errorf("expected '1.2.3', got %q", e.Version())
	}
}

func TestParseChangelog_Unreleased(t *testing.T) {
	content := `## [Unreleased]

### Added
- New feature

## [1.0.0] - 2026-01-01

### Fixed
- Bug fix
`
	entries := ParseChangelogContent(content)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Unreleased should sort after versioned entries (highest version number).
	if entries[0].Major != 999 {
		t.Errorf("expected unreleased entry first (newest-first), got %s", entries[0].Version())
	}
	if !strings.Contains(entries[0].Content, "New feature") {
		t.Error("unreleased entry should contain 'New feature'")
	}
	if entries[1].Version() != "1.0.0" {
		t.Errorf("expected 1.0.0, got %s", entries[1].Version())
	}
}
