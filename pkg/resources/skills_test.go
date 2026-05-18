package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFrontmatterSimple(t *testing.T) {
	content := `---
name: my-skill
description: A test skill
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
	}

	prompt := FormatSkillsForPrompt(skills)
	if !strings.Contains(prompt, "coding") {
		t.Error("should contain coding skill")
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
		{"my-skill", "other-dir", 1},  // name mismatch
		{"UPPER", "UPPER", 1},         // invalid chars
		{"-start", "-start", 1},       // starts with hyphen
		{"end-", "end-", 1},           // ends with hyphen
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

func TestLoadSkills_DuplicateNameCoexistence(t *testing.T) {
	// Two roots with same-named skills now coexist (no shadowing); a
	// "duplicate-name" diagnostic surfaces the ambiguity.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	skill1Dir := filepath.Join(dir1, "myskill")
	os.MkdirAll(skill1Dir, 0755)
	os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte("---\nname: myskill\ndescription: First one\n---\nContent1"), 0644)

	skill2Dir := filepath.Join(dir2, "myskill")
	os.MkdirAll(skill2Dir, 0755)
	os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte("---\nname: myskill\ndescription: Second one\n---\nContent2"), 0644)

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             t.TempDir(),
		IncludeDefaults: false,
		SkillPaths:      []string{dir1, dir2},
	})

	if len(result.Skills) != 2 {
		t.Fatalf("expected both skills to coexist, got %d", len(result.Skills))
	}
	// Two distinct path:<basename> origins → distinct IDs.
	if result.Skills[0].ID == result.Skills[1].ID {
		t.Errorf("expected distinct IDs, both = %q", result.Skills[0].ID)
	}

	var dup ResourceDiagnostic
	for _, d := range result.Diagnostics {
		if d.Type == "duplicate-name" {
			dup = d
			break
		}
	}
	if dup.Type == "" {
		t.Fatalf("expected duplicate-name diagnostic, got %+v", result.Diagnostics)
	}
	if !strings.Contains(dup.Message, "myskill") {
		t.Errorf("diagnostic should mention skill name; got %q", dup.Message)
	}
}

func TestLoadSkills_OverrideTrueExplicit(t *testing.T) {
	// override: <full-id> shadows that exact target only.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	dir3 := t.TempDir()

	makeSkill := func(root, body string) {
		sd := filepath.Join(root, "shared")
		os.MkdirAll(sd, 0o755)
		os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o644)
	}
	makeSkill(dir1, "---\nname: shared\ndescription: A\n---\n")
	makeSkill(dir2, "---\nname: shared\ndescription: B\n---\n")
	// Skill in dir3 overrides only the dir1 variant.
	makeSkill(dir3, "---\nname: shared\ndescription: C\noverride: path:"+filepath.Base(dir1)+"__shared\n---\n")

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             t.TempDir(),
		IncludeDefaults: false,
		SkillPaths:      []string{dir1, dir2, dir3},
	})
	if len(result.Skills) != 2 {
		t.Fatalf("expected 2 surviving skills (dir2 + dir3), got %d: %+v", len(result.Skills), result.Skills)
	}
	dir1ID := MakeSkillID("path:"+filepath.Base(dir1), "shared")
	for _, s := range result.Skills {
		if s.ID == dir1ID {
			t.Errorf("dir1 skill should have been overridden, but survived: %+v", s)
		}
	}
}

// TestLoadSkills_OverrideTrueConflict verifies that when two skills both
// declare `override: true` for the same name, origin precedence (user >
// project > path:* > pkg:* > builtin) picks the winner and an
// override-conflict diagnostic surfaces the losers.
func TestLoadSkills_OverrideTrueConflict(t *testing.T) {
	dir1 := t.TempDir() // path:<basename> origin (precedence 3)
	dir2 := t.TempDir() // another path:<basename> origin (precedence 3, tie → lower index wins)

	makeSkill := func(root string) {
		sd := filepath.Join(root, "rival")
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: rival\ndescription: from " + filepath.Base(root) + "\noverride: true\n---\n"
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	makeSkill(dir1)
	makeSkill(dir2)

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             t.TempDir(),
		IncludeDefaults: false,
		SkillPaths:      []string{dir1, dir2},
	})

	if len(result.Skills) != 1 {
		t.Fatalf("expected exactly 1 surviving skill after override:true conflict, got %d: %+v", len(result.Skills), result.Skills)
	}
	// dir1 came first so it wins on tie.
	wantID := MakeSkillID("path:"+filepath.Base(dir1), "rival")
	if result.Skills[0].ID != wantID {
		t.Errorf("winner ID = %q, want %q (first-encountered wins on precedence tie)", result.Skills[0].ID, wantID)
	}

	var sawConflict bool
	for _, d := range result.Diagnostics {
		if d.Type == "override-conflict" && strings.Contains(d.Message, "rival") {
			sawConflict = true
		}
	}
	if !sawConflict {
		t.Errorf("expected override-conflict diagnostic, got %+v", result.Diagnostics)
	}
}

// TestLoadSkills_StableOrder verifies LoadSkills returns skills in
// alphabetical order so the system prompt's <available_skills> block
// stays byte-stable across process restarts and Reload calls. Map
// iteration is randomised, so without an explicit sort the order
// would drift and bust the prompt cache.
func TestLoadSkills_StableOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zebra", "apple", "mango", "banana"} {
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: skill " + name + "\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"apple", "banana", "mango", "zebra"}

	// Run several times — map iteration order is randomised per process,
	// but the result must be identical.
	for i := 0; i < 5; i++ {
		result := LoadSkills(LoadSkillsOptions{Cwd: dir, SkillPaths: []string{dir}})
		if len(result.Skills) != len(want) {
			t.Fatalf("iter %d: got %d skills, want %d", i, len(result.Skills), len(want))
		}
		for j, w := range want {
			if result.Skills[j].Name != w {
				t.Errorf("iter %d: skill[%d].Name = %q, want %q", i, j, result.Skills[j].Name, w)
			}
		}
	}
}

// TestLoadSkillsFromDir_SymlinkedSkillFile: a SKILL.md that is a symlink to
// a real file in another directory should be loaded.
func TestLoadSkillsFromDir_SymlinkedSkillFile(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()

	realFile := filepath.Join(external, "real-skill.md")
	content := "---\nname: linked\ndescription: linked skill\n---\nbody\n"
	if err := os.WriteFile(realFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(root, "linked")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	result := LoadSkillsFromDir(root, "project")
	found := false
	for _, s := range result.Skills {
		if s.Name == "linked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symlinked SKILL.md not loaded; got %+v", result.Skills)
	}
}

// TestLoadSkillsFromDir_SymlinkedSubdir: a subdirectory entry that is a
// symlink to another directory containing SKILL.md should recurse into it.
func TestLoadSkillsFromDir_SymlinkedSubdir(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()

	realDir := filepath.Join(external, "mango")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: mango\ndescription: mango skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(realDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(realDir, filepath.Join(root, "mango")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	result := LoadSkillsFromDir(root, "project")
	found := false
	for _, s := range result.Skills {
		if s.Name == "mango" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symlinked subdir not recursed; got %+v", result.Skills)
	}
}

// TestLoadSkillsFromDir_SymlinkCycle: a symlink cycle must not hang the
// loader.
func TestLoadSkillsFromDir_SymlinkCycle(t *testing.T) {
	root := t.TempDir()

	subA := filepath.Join(root, "a")
	if err := os.MkdirAll(subA, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: a\ndescription: a skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(subA, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a cycle: a/loop -> root, which contains a, which contains loop, ...
	if err := os.Symlink(root, filepath.Join(subA, "loop")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	done := make(chan LoadSkillsResult, 1)
	go func() {
		done <- LoadSkillsFromDir(root, "project")
	}()
	select {
	case result := <-done:
		found := false
		for _, s := range result.Skills {
			if s.Name == "a" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected skill 'a' despite cycle; got %+v", result.Skills)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loader hung on symlink cycle")
	}
}

func TestDisplayOrigin(t *testing.T) {
	cases := []struct {
		name string
		in   Skill
		want string
	}{
		{"builtin", Skill{Source: "builtin", FilePath: "/tmp/xyz/SKILL.md"}, "built-in"},
		{"project", Skill{Source: "project", FilePath: "/home/x/proj/.fir/skills/foo/SKILL.md"}, "project"},
		{"user", Skill{Source: "user", FilePath: "/home/x/.config/fir/skills/foo/SKILL.md"}, "user"},
		{"package", Skill{Source: "package", FilePath: "/home/x/.config/fir/packages/git/github.com/foo/bar/skills/baz/SKILL.md"}, "user"},
		{"path", Skill{Source: "path", FilePath: "/somewhere/else/skill.md"}, "/somewhere/else/skill.md"},
		{"unknown source falls back to path", Skill{Source: "weird", FilePath: "/x/y"}, "/x/y"},
		{"unknown source no path falls back to source", Skill{Source: "weird"}, "weird"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayOrigin(c.in); got != c.want {
				t.Fatalf("DisplayOrigin(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
