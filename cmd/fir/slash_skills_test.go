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

func TestResolveSlashInvocation_UnknownHeadless(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)
	args := &Args{Messages: []string{"/definitely-not-a-skill", "do", "stuff"}}
	err := resolveSlashInvocation(args, true)
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	if !strings.Contains(err.Error(), "unknown skill or command") {
		t.Errorf("error %v, want contains 'unknown skill or command'", err)
	}
}

func TestResolveSlashInvocation_NonSlash(t *testing.T) {
	args := &Args{Messages: []string{"hello world"}}
	if err := resolveSlashInvocation(args, true); err != nil {
		t.Fatal(err)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "hello world" {
		t.Errorf("messages mutated: %v", args.Messages)
	}
}

func TestResolveSlashInvocation_SkillFound(t *testing.T) {
	dir := writeDemoSkill(t)
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/demo", "build", "the", "thing"}}
	if err := resolveSlashInvocation(args, true); err != nil {
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
	if err := resolveSlashInvocation(args, true); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if len(args.Messages) != 1 || !strings.Contains(args.Messages[0], "Task: do the thing") {
		t.Errorf("messages = %v", args.Messages)
	}
	if !args.Print {
		t.Error("Print flag lost across rewrite")
	}
}

// TestResolveSlashInvocation_SkillWinsOverCommand pins the back-compat
// resolution order: a skill named like a builtin slash command still resolves
// to the skill.
func TestResolveSlashInvocation_SkillWinsOverCommand(t *testing.T) {
	dir := writeNamedSkill(t, "changelog")
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/changelog", "please"}}
	if err := resolveSlashInvocation(args, false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(args.Messages) != 1 || !strings.Contains(args.Messages[0], "skill") {
		t.Fatalf("expected skill directive, got %v", args.Messages)
	}
	if !strings.Contains(args.Messages[0], "Task: please") {
		t.Errorf("task body lost: %q", args.Messages[0])
	}
}

// TestResolveSlashInvocation_BuiltinCommandInteractive verifies a builtin
// slash command with no same-named skill is passed through verbatim so the
// interactive mode can dispatch it.
func TestResolveSlashInvocation_BuiltinCommandInteractive(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/model", "sonnet"}}
	if err := resolveSlashInvocation(args, false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "/model sonnet" {
		t.Errorf("messages = %v, want [/model sonnet]", args.Messages)
	}
}

// TestResolveSlashInvocation_BuiltinCommandHeadless verifies headless modes
// reject TUI-bound commands with a clear message instead of hanging or
// sending the literal text to the model.
func TestResolveSlashInvocation_BuiltinCommandHeadless(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/changelog"}}
	err := resolveSlashInvocation(args, true)
	if err == nil {
		t.Fatal("expected error in headless mode")
	}
	if !strings.Contains(err.Error(), "requires interactive mode") {
		t.Errorf("error = %v, want 'requires interactive mode'", err)
	}
}

// TestResolveSlashInvocation_UnknownInteractive verifies unknown names are
// passed through in interactive mode — they may be extension commands, whose
// names are only known after the extension handshake.
func TestResolveSlashInvocation_UnknownInteractive(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)

	args := &Args{Messages: []string{"/maybe-an-extension", "arg"}}
	if err := resolveSlashInvocation(args, false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "/maybe-an-extension arg" {
		t.Errorf("messages = %v", args.Messages)
	}
}

// TestResolveSlashInvocation_SkillPrefixUntouched verifies /skill:<name> is
// left for the session to expand, in every mode.
func TestResolveSlashInvocation_SkillPrefixUntouched(t *testing.T) {
	dir := t.TempDir()
	isolatedSkillEnv(t, dir)

	for _, headless := range []bool{false, true} {
		args := &Args{Messages: []string{"/skill:demo", "go"}}
		if err := resolveSlashInvocation(args, headless); err != nil {
			t.Fatalf("headless=%v: %v", headless, err)
		}
		if len(args.Messages) != 2 || args.Messages[0] != "/skill:demo" {
			t.Errorf("headless=%v: messages mutated: %v", headless, args.Messages)
		}
	}
}

// writeNamedSkill creates a temporary project directory containing a skill
// with the given name and chdirs into it.
func writeNamedSkill(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".fir", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: A test skill.\n---\n\nDo things.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirFor(t, dir)
	return dir
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
