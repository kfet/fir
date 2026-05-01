package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasSlashSkillPrefix(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"/":        false,
		"/work":    true,
		"/wt-foo":  true,
		"/path/to": false, // looks like a real path
		"work":     false,
		"-p":       false,
	}
	for in, want := range cases {
		if got := hasSlashSkillPrefix(in); got != want {
			t.Errorf("hasSlashSkillPrefix(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRewriteSlashSkillMessages_Unknown(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)
	args := &Args{Messages: []string{"/definitely-not-a-skill", "do", "stuff"}}
	err := rewriteSlashSkillMessages(args)
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("error %v, want contains 'unknown skill'", err)
	}
}

func TestRewriteSlashSkillMessages_NonSlash(t *testing.T) {
	args := &Args{Messages: []string{"hello world"}}
	if err := rewriteSlashSkillMessages(args); err != nil {
		t.Fatal(err)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "hello world" {
		t.Errorf("messages mutated: %v", args.Messages)
	}
}

func TestRewriteSlashSkillMessages_Found(t *testing.T) {
	dir := writeDemoSkill(t)
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/demo", "build", "the", "thing"}}
	if err := rewriteSlashSkillMessages(args); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if len(args.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %v", len(args.Messages), args.Messages)
	}
	got := args.Messages[0]
	if !strings.Contains(got, "demo") || !strings.Contains(got, "Task: build the thing") {
		t.Errorf("rewrite produced unexpected message: %q", got)
	}
}

// TestParseArgsThenSlashRewrite_ComposesWithFlags verifies the slash-skill
// rewrite leaves other parsed flags (e.g. -p) intact — i.e. it composes with
// ParseArgs rather than fighting it.
func TestParseArgsThenSlashRewrite_ComposesWithFlags(t *testing.T) {
	dir := writeDemoSkill(t)
	isolatedSkillEnv(t, dir)

	args := ParseArgs([]string{"-p", "/demo", "do", "the", "thing"})
	if !args.Print {
		t.Fatalf("expected -p to set Print; args=%+v", args)
	}
	if err := rewriteSlashSkillMessages(args); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if len(args.Messages) != 1 || !strings.Contains(args.Messages[0], "Task: do the thing") {
		t.Errorf("messages = %v", args.Messages)
	}
	if !args.Print {
		t.Error("Print flag lost across rewrite")
	}
}

// writeDemoSkill creates a temporary project directory containing a
// `.fir/skills/demo/SKILL.md` and chdirs into it.
func writeDemoSkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".fir", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: demo\ndescription: A demo skill for tests.\n---\n\nDo demo things.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirFor(t, dir)
	return dir
}

// isolatedSkillEnv points FIR_AGENT_DIR at an empty temp dir so skill loads
// don't leak the developer's real `~/.config/fir/skills/`.
func isolatedSkillEnv(t *testing.T, projectDir string) {
	t.Helper()
	t.Setenv("FIR_AGENT_DIR", filepath.Join(projectDir, "_agentdir"))
	if err := os.MkdirAll(filepath.Join(projectDir, "_agentdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdirFor(t, projectDir)
}

// chdirFor changes into dir for the duration of the test.
func chdirFor(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
