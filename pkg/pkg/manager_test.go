package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mockSettings implements SettingsBackend entirely in memory.
type mockSettings struct {
	global  []any
	project []any
}

func (m *mockSettings) GetGlobalPackages() []any   { return m.global }
func (m *mockSettings) SetGlobalPackages(p []any)  { m.global = p }
func (m *mockSettings) GetProjectPackages() []any  { return m.project }
func (m *mockSettings) SetProjectPackages(p []any) { m.project = p }

// initBareRepo creates a minimal bare git repository at dir so that
// "git clone" can use it as a source in tests.
func initBareRepo(t *testing.T, dir string) {
	t.Helper()

	// Init a normal repo, add a commit, then convert to bare.
	tmp := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")

	// Write a skill file so ScanPackageResources finds something.
	skillDir := filepath.Join(tmp, "myskill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# skill"), 0644); err != nil {
		t.Fatal(err)
	}

	run("add", ".")
	run("commit", "-m", "init")

	// Clone as bare into dir.
	cmd := exec.Command("git", "clone", "--bare", tmp, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
}

func TestInstallUnlist(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()
	sm := &mockSettings{}
	mgr := New(agentDir, cwd, sm)

	// Create a fake bare git repo to clone from.
	bareRepo := t.TempDir()
	initBareRepo(t, bareRepo)

	// We install using the bare repo path as a local source so no
	// network is needed.  But we want to exercise the git code path,
	// so craft a URL that points at the local bare repo.
	// Use "file://" scheme — ParseSource only handles http/ssh/bare,
	// so we construct an HTTPS-style URL manually instead by directly
	// calling Clone + addPackage to keep the test self-contained.
	//
	// Simpler: use a local path source (type==local) pointing at the
	// working copy we just cloned from the bare repo.

	// Clone the bare repo into a working dir first.
	workRepo := filepath.Join(agentDir, "work-clone")
	if err := Clone(bareRepo, workRepo); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Install via local path.
	localSrc := "./" + workRepo // will be resolved to absolute
	if err := mgr.Install(workRepo, false); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Should appear in global packages.
	pkgs, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("List: want 1 package, got %d", len(pkgs))
	}
	if pkgs[0].Scope != "user" {
		t.Errorf("Scope: got %q, want %q", pkgs[0].Scope, "user")
	}

	// Install same package again — should be idempotent.
	_ = localSrc
	if err := mgr.Install(workRepo, false); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	pkgs, _ = mgr.List()
	if len(pkgs) != 1 {
		t.Errorf("idempotent install: want 1 package, got %d", len(pkgs))
	}

	// Resolve should surface the SKILL.md.
	rr, err := mgr.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rr.Skills) == 0 {
		t.Errorf("Resolve: expected at least 1 skill, got 0")
	}

	// Uninstall.
	if err := mgr.Uninstall(workRepo, false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	pkgs, _ = mgr.List()
	if len(pkgs) != 0 {
		t.Errorf("after Uninstall: want 0 packages, got %d", len(pkgs))
	}
}

func TestInstallProjectScope(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()
	sm := &mockSettings{}
	mgr := New(agentDir, cwd, sm)

	// Create a simple local dir with a skill.
	pkgDir := t.TempDir()
	skillDir := filepath.Join(pkgDir, "askill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# hi"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Install(pkgDir, true); err != nil {
		t.Fatalf("Install project: %v", err)
	}

	pkgs, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("List: want 1, got %d", len(pkgs))
	}
	if pkgs[0].Scope != "project" {
		t.Errorf("Scope: got %q, want project", pkgs[0].Scope)
	}
}

func TestUpdateAll(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()

	// Create a bare repo and a clone.
	bareRepo := t.TempDir()
	initBareRepo(t, bareRepo)
	workRepo := filepath.Join(agentDir, "update-clone")
	if err := Clone(bareRepo, workRepo); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	sm := &mockSettings{}
	mgr := New(agentDir, cwd, sm)
	if err := mgr.Install(workRepo, false); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Update all — workRepo is local type so Update skips it (only git type pulled).
	// Just ensure Update returns no error.
	if err := mgr.Update(""); err != nil {
		t.Errorf("Update: %v", err)
	}
}

func TestListEmpty(t *testing.T) {
	sm := &mockSettings{}
	mgr := New(t.TempDir(), t.TempDir(), sm)
	pkgs, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("want 0, got %d", len(pkgs))
	}
}

func TestResolveEmpty(t *testing.T) {
	sm := &mockSettings{}
	mgr := New(t.TempDir(), t.TempDir(), sm)
	rr, err := mgr.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Extensions)+len(rr.Skills)+len(rr.Prompts)+len(rr.Themes) != 0 {
		t.Error("expected all empty slices")
	}
}
