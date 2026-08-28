package main

import (
	"os"
	"path/filepath"
	"testing"
)

// loadCLISkills must honour the project settings.json "skills" list, the same
// way a live session does. Regression: `fir skills list` used to call
// resources.LoadSkills with defaults only, silently ignoring configured dirs.
func TestLoadCLISkillsHonoursSettingsSkillPaths(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	mkSkill := func(dir, name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + name + " skill\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkSkill(filepath.Join(work, "skills"), "alpha")
	mkSkill(filepath.Join(root, "daisy-skills", "skills"), "beta")
	mkSkill(filepath.Join(work, ".fir", "skills"), "proj")

	settings := `{"skills": ["./skills", "../daisy-skills/skills"]}`
	if err := os.WriteFile(filepath.Join(work, ".fir", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FIR_AGENT_DIR", filepath.Join(root, "agent"))
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, s := range loadCLISkills() {
		got[s.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "proj"} {
		if !got[want] {
			t.Errorf("skill %q not loaded by loadCLISkills; got %v", want, got)
		}
	}
}
