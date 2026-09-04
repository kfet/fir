package resources

import (
	"os"
	"path/filepath"
	"strings"
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
		"aside-advisor":       false,
		"acp-shepherd":        false,
		"autoresearch-create": false,
		"claude-usage":        false,
		"codex-usage":         false,
		"extension-creator":   false,
		"instruction-tune":    false,
		"loop":                false,
		"merge-to-main":       false,
		"notify":              false,
		"poe-models":          false,
		"poe-usage":           false,
		"project-ops":         false,
		"rebase-on-main":      false,
		"research":            false,
		"review-and-fix":      false,
		"self":                false,
		"shepherd":            false,
		"ship-it":             false,
		"ship-wt":             false,
		"skill-creator":       false,
		"tmux-driver":         false,
		"tmux-observer":       false,
		"wrap-up":             false,
		"wt":                  false,
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

func TestLoadSkills_UserCoexistsWithBuiltin(t *testing.T) {
	// A user skill with the same name as a builtin coexists by default.
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	skillDir := filepath.Join(agentDir, "skills", "research")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: User research skill (coexists with builtin)
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

	var sawUser, sawBuiltin bool
	for _, s := range result.Skills {
		if s.Name != "research" {
			continue
		}
		switch s.Origin {
		case "user":
			sawUser = true
			if s.ID != "user__research" {
				t.Errorf("user research ID=%q, want user__research", s.ID)
			}
		case "builtin":
			sawBuiltin = true
			if s.ID != "builtin__research" {
				t.Errorf("builtin research ID=%q, want builtin__research", s.ID)
			}
		}
	}
	if !sawUser || !sawBuiltin {
		t.Fatalf("expected both user and builtin research skills to coexist; sawUser=%v sawBuiltin=%v", sawUser, sawBuiltin)
	}

	// And we should get a duplicate-name diagnostic.
	var sawDup bool
	for _, d := range result.Diagnostics {
		if d.Type == "duplicate-name" && strings.Contains(d.Message, `"research"`) {
			sawDup = true
		}
	}
	if !sawDup {
		t.Errorf("expected duplicate-name diagnostic for research; got %+v", result.Diagnostics)
	}
}

func TestLoadSkills_UserOverrideTrueShadowsBuiltin(t *testing.T) {
	// override: true on a user skill replaces any other same-named skill.
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	skillDir := filepath.Join(agentDir, "skills", "research")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: User override of research skill
override: true
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

	var researches []Skill
	for _, s := range result.Skills {
		if s.Name == "research" {
			researches = append(researches, s)
		}
	}
	if len(researches) != 1 {
		t.Fatalf("expected exactly one research skill after override, got %d", len(researches))
	}
	if researches[0].Origin != "user" {
		t.Errorf("surviving research origin=%q, want user", researches[0].Origin)
	}
	if got := researches[0].Description; got != "User override of research skill" {
		t.Errorf("description=%q", got)
	}
	// Surviving skill should record what it overrode.
	var overrodeBuiltin bool
	for _, id := range researches[0].Overridden {
		if id == "builtin__research" {
			overrodeBuiltin = true
		}
	}
	if !overrodeBuiltin {
		t.Errorf("expected Overridden to contain builtin__research, got %v", researches[0].Overridden)
	}
	// An explicit `override: true` is intentional — no shadow warning should
	// be emitted. Only ambiguous/unintentional collisions deserve a diag.
	for _, d := range result.Diagnostics {
		if d.Type == "shadowed" {
			t.Errorf("unexpected shadowed diagnostic for explicit override: %+v", d)
		}
	}
}

func TestLoadSkills_ProjectCoexistsWithBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(tmpDir, ".fir", "skills", "notify")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
description: Project notify variant
---
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             tmpDir,
		AgentDir:        agentDir,
		IncludeDefaults: true,
	})

	var sawProject, sawBuiltin bool
	for _, s := range result.Skills {
		if s.Name != "notify" {
			continue
		}
		switch s.Origin {
		case "project":
			sawProject = true
		case "builtin":
			sawBuiltin = true
		}
	}
	if !sawProject || !sawBuiltin {
		t.Fatalf("expected project and builtin notify skills to coexist; sawProject=%v sawBuiltin=%v", sawProject, sawBuiltin)
	}
}

// TestLoadSkills_ProjectMirrorOfBuiltinShadows verifies the in-repo
// convention where `.fir/skills` is symlinked at (or copied from) the
// builtin source tree: each builtin SKILL.md carries `override: true` in its
// frontmatter so the project-origin copy shadows the builtin-origin copy.
// The builtin self-load must drop its own override claim, otherwise both
// copies would claim it and an override-conflict diagnostic would fire.
func TestLoadSkills_ProjectMirrorOfBuiltinShadows(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Mirror the builtin "notify" skill — same name, with override: true,
	// as every builtin SKILL.md now carries.
	skillDir := filepath.Join(tmpDir, ".fir", "skills", "notify")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: notify
description: Project mirror of builtin notify
builtin: true
override: true
---
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             tmpDir,
		AgentDir:        agentDir,
		IncludeDefaults: true,
	})

	var notifies []Skill
	for _, s := range result.Skills {
		if s.Name == "notify" {
			notifies = append(notifies, s)
		}
	}
	if len(notifies) != 1 {
		t.Fatalf("expected exactly one notify skill after project override, got %d", len(notifies))
	}
	if notifies[0].Origin != "project" {
		t.Errorf("surviving notify origin=%q, want project", notifies[0].Origin)
	}
	var overrodeBuiltin bool
	for _, id := range notifies[0].Overridden {
		if id == "builtin__notify" {
			overrodeBuiltin = true
		}
	}
	if !overrodeBuiltin {
		t.Errorf("expected Overridden to contain builtin__notify, got %v", notifies[0].Overridden)
	}

	// And no override-conflict diagnostic should fire.
	for _, d := range result.Diagnostics {
		if d.Type == "override-conflict" && strings.Contains(d.Message, "notify") {
			t.Errorf("unexpected override-conflict diagnostic: %s", d.Message)
		}
	}

	// Also assert: with no project mirror, the agent listing must not
	// contain any duplicate-name diagnostics for builtin skills against
	// themselves — i.e. the builtin self-load is single-origin.
	plain := LoadSkills(LoadSkillsOptions{
		Cwd:             t.TempDir(),
		AgentDir:        filepath.Join(tmpDir, "agent-empty"),
		IncludeDefaults: true,
	})
	for _, d := range plain.Diagnostics {
		if d.Type == "duplicate-name" {
			t.Errorf("unexpected duplicate-name diagnostic on plain load: %s", d.Message)
		}
		if d.Type == "override-conflict" {
			t.Errorf("unexpected override-conflict diagnostic on plain load: %s", d.Message)
		}
	}
}
