package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBuiltinSkills_ReturnsExpectedSkills(t *testing.T) {
	result := LoadBuiltinSkills()

	if len(result.Diagnostics) > 0 {
		for _, d := range result.Diagnostics {
			t.Errorf("unexpected diagnostic: %s: %s", d.Type, d.Message)
		}
	}

	expected := map[string]bool{
		"claude-usage":      false,
		"extension-creator": false,
		"fix":               false,
		"loop":              false,
		"monitor":           false,
		"notify":            false,
		"overseer":          false,
		"research":          false,
		"review":            false,
		"self":              false,
		"skill-creator":     false,
		"tmux-driver":       false,
	}

	for _, s := range result.Skills {
		if _, ok := expected[s.Name]; !ok {
			t.Errorf("unexpected builtin skill: %s", s.Name)
			continue
		}
		expected[s.Name] = true

		if s.Source != "builtin" {
			t.Errorf("skill %s: source=%q, want %q", s.Name, s.Source, "builtin")
		}
		if s.Description == "" {
			t.Errorf("skill %s: empty description", s.Name)
		}
		if s.FilePath == "" {
			t.Errorf("skill %s: empty FilePath", s.Name)
		}
		if s.BaseDir == "" {
			t.Errorf("skill %s: empty BaseDir", s.Name)
		}
		// Verify extracted file exists on disk
		if _, err := os.Stat(s.FilePath); err != nil {
			t.Errorf("skill %s: FilePath %s does not exist: %v", s.Name, s.FilePath, err)
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing expected builtin skill: %s", name)
		}
	}
}

func TestLoadSkills_UserOverridesBuiltin(t *testing.T) {
	// Create a temp dir with a user skill that has the same name as a builtin
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	skillDir := filepath.Join(agentDir, "skills", "research")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: User override of research skill
---
Custom research skill.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             tmpDir,
		AgentDir:        agentDir,
		IncludeDefaults: true,
	})

	var researchSkill *Skill
	for i := range result.Skills {
		if result.Skills[i].Name == "research" {
			researchSkill = &result.Skills[i]
			break
		}
	}

	if researchSkill == nil {
		t.Fatal("research skill not found")
	}
	if researchSkill.Source != "user" {
		t.Errorf("research skill source=%q, want %q (user should override builtin)", researchSkill.Source, "user")
	}
	if researchSkill.Description != "User override of research skill" {
		t.Errorf("research skill has wrong description: %s", researchSkill.Description)
	}
}

func TestLoadSkills_ProjectOverridesBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(tmpDir, ".fir", "skills", "loop")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: Project loop override
---
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             tmpDir,
		AgentDir:        agentDir,
		IncludeDefaults: true,
	})

	for _, s := range result.Skills {
		if s.Name == "loop" {
			if s.Source != "project" {
				t.Errorf("loop skill source=%q, want %q", s.Source, "project")
			}
			return
		}
	}
	t.Fatal("loop skill not found")
}
