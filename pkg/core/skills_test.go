package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatterSimple(t *testing.T) {
	content := `---
name: my-skill
description: A test skill
disable-model-invocation: true
---
# Skill content here
`
	fm := parseFrontmatterSimple(content)
	if fm.Name != "my-skill" {
		t.Errorf("name = %q, want my-skill", fm.Name)
	}
	if fm.Description != "A test skill" {
		t.Errorf("description = %q", fm.Description)
	}
	if !fm.DisableModelInvocation {
		t.Error("expected disableModelInvocation = true")
	}
}

func TestParseFrontmatterSimple_NoFrontmatter(t *testing.T) {
	fm := parseFrontmatterSimple("# Just markdown")
	if fm.Name != "" {
		t.Errorf("name = %q, want empty", fm.Name)
	}
}

func TestLoadSkillsFromDir(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory with SKILL.md
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: Test skill description
---
# My Skill
Instructions here.
`), 0644)

	result := LoadSkillsFromDir(dir, "test")
	if len(result.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(result.Skills))
	}
	if result.Skills[0].Name != "my-skill" {
		t.Errorf("name = %q", result.Skills[0].Name)
	}
	if result.Skills[0].Description != "Test skill description" {
		t.Errorf("description = %q", result.Skills[0].Description)
	}
}

func TestLoadSkillsFromDir_RootMd(t *testing.T) {
	dir := t.TempDir()

	// Create a root .md file (direct child)
	os.WriteFile(filepath.Join(dir, "coding.md"), []byte(`---
name: coding
description: Coding instructions
---
# Coding
`), 0644)

	result := LoadSkillsFromDir(dir, "test")
	if len(result.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d (diags: %v)", len(result.Skills), result.Diagnostics)
	}
}

func TestLoadSkillsFromDir_Empty(t *testing.T) {
	dir := t.TempDir()
	result := LoadSkillsFromDir(dir, "test")
	if len(result.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(result.Skills))
	}
}

func TestLoadSkillsFromDir_MissingDescription(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "bad-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: bad-skill
---
No description in frontmatter.
`), 0644)

	result := LoadSkillsFromDir(dir, "test")
	if len(result.Skills) != 0 {
		t.Errorf("expected 0 skills (missing description), got %d", len(result.Skills))
	}
	if len(result.Diagnostics) == 0 {
		t.Error("expected diagnostics for missing description")
	}
}

func TestFormatSkillsForPrompt(t *testing.T) {
	skills := []Skill{
		{
			Name:        "coding",
			Description: "Coding best practices",
			FilePath:    "/path/to/coding/SKILL.md",
		},
		{
			Name:                   "hidden",
			Description:            "Hidden skill",
			FilePath:               "/path/to/hidden/SKILL.md",
			DisableModelInvocation: true,
		},
	}

	prompt := FormatSkillsForPrompt(skills)
	if !strings.Contains(prompt, "coding") {
		t.Error("should contain coding skill")
	}
	if strings.Contains(prompt, "hidden") {
		t.Error("should not contain hidden skill")
	}
	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("should contain XML tags")
	}
}

func TestFormatSkillsForPrompt_Empty(t *testing.T) {
	prompt := FormatSkillsForPrompt(nil)
	if prompt != "" {
		t.Errorf("expected empty prompt for no skills, got %q", prompt)
	}
}

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name      string
		parentDir string
		wantErrs  int
	}{
		{"my-skill", "my-skill", 0},
		{"my-skill", "other-dir", 1}, // name mismatch
		{"UPPER", "UPPER", 1},        // invalid chars
		{"-start", "-start", 1},      // starts with hyphen
		{"end-", "end-", 1},          // ends with hyphen
		{"bad--name", "bad--name", 1}, // consecutive hyphens
	}

	for _, tt := range tests {
		errs := validateSkillName(tt.name, tt.parentDir)
		if len(errs) < tt.wantErrs {
			t.Errorf("validateSkillName(%q, %q) = %d errors, want >= %d", tt.name, tt.parentDir, len(errs), tt.wantErrs)
		}
	}
}

func TestEscapeXml(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"<script>", "&lt;script&gt;"},
		{`a&b"c'`, `a&amp;b&quot;c&apos;`},
	}
	for _, tt := range tests {
		got := escapeXml(tt.input)
		if got != tt.want {
			t.Errorf("escapeXml(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
