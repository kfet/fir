package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initSubdirRepo creates a bare repo named "exts" under base containing two
// subdirectories, each holding one skill, and points git at it so that
// "https://github.com/testorg/exts" resolves to the local bare repo.
// It returns the source prefix to install from.
func initSubdirRepo(t *testing.T, base string) string {
	t.Helper()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	work := filepath.Join(base, "work")
	for _, s := range []string{"reminders/rem", "triage/tri"} {
		dir := filepath.Join(work, s)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# skill"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run(work, "init", "-q", "-b", "main", ".")
	run(work, "add", ".")
	run(work, "commit", "-qm", "init")
	run(work, "tag", "v1")
	run(base, "clone", "-q", "--bare", work, "exts")

	// Rewrite the canonical GitHub URL to the local bare repo.
	cfg := filepath.Join(base, "gitconfig")
	content := "[url \"" + base + "/\"]\n\tinsteadOf = https://github.com/testorg/\n"
	if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	return "github.com/testorg/exts"
}

func newSubdirManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	m := New(agentDir, t.TempDir(), &mockSettings{})
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")
	return m, repo, clone
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func TestInstallTwoSubdirsOfSameRepo(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatalf("install reminders: %v", err)
	}
	if err := m.Install(repo+"/triage", false); err != nil {
		t.Fatalf("install triage: %v", err)
	}

	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))

	pkgs, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
	for _, p := range pkgs {
		if p.Resources == nil || len(p.Resources.Skills) != 1 {
			t.Fatalf("package %s discovered no skill: %+v", p.Source.Raw, p.Resources)
		}
	}
}

func TestInstallMissingSubdirFailsLoudly(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	err := m.Install(repo+"/nosuchdir", false)
	if err == nil {
		t.Fatal("expected error installing a non-existent subdirectory")
	}
	if len(m.sm.GetGlobalPackages()) != 0 {
		t.Fatalf("failed install must not register the package: %v", m.sm.GetGlobalPackages())
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("failed install must not leave a stray clone at %s, err=%v", clone, err)
	}
}

func TestInstallTwoSubdirsPinnedRef(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders@v1", false); err != nil {
		t.Fatalf("install reminders@v1: %v", err)
	}
	// The clone has a detached HEAD here, so "git pull" would fail.
	if err := m.Install(repo+"/triage@v1", false); err != nil {
		t.Fatalf("install triage@v1: %v", err)
	}
	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))

	if err := m.Update(""); err != nil {
		t.Fatalf("update pinned packages: %v", err)
	}
}

func TestInstallConflictingRefsRejected(t *testing.T) {
	m, repo, _ := newSubdirManager(t)

	if err := m.Install(repo+"/reminders@v1", false); err != nil {
		t.Fatal(err)
	}
	err := m.Install(repo+"/triage@main", false)
	if err == nil {
		t.Fatal("expected an error when two subdirs of one repo want different refs")
	}
	if !strings.Contains(err.Error(), "ref") {
		t.Fatalf("error should explain the ref conflict, got: %v", err)
	}
}

func TestUninstallMatchesAlternateSpelling(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall("https://"+repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if got := m.sm.GetGlobalPackages(); len(got) != 0 {
		t.Fatalf("package should be deregistered, got %v", got)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("clone %s should be removed, err=%v", clone, err)
	}
}

func TestInstallWholeRepoAfterSubdir(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(repo, false); err != nil {
		t.Fatalf("install whole repo: %v", err)
	}
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))
}

func TestUninstallSubdirKeepsSibling(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(repo+"/triage", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(repo+"/reminders", false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))
	if _, err := os.Stat(filepath.Join(clone, "reminders")); !os.IsNotExist(err) {
		t.Fatalf("expected reminders subdir to be pruned, err=%v", err)
	}

	// Removing the last package deletes the shared clone.
	if err := m.Uninstall(repo+"/triage", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("expected clone %s to be removed, err=%v", clone, err)
	}
}

func TestUpdateSubdirPackages(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(repo+"/triage", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(""); err != nil {
		t.Fatalf("update: %v", err)
	}
	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))
}

func TestSparseAddOnFullClone(t *testing.T) {
	base := t.TempDir()
	initSubdirRepo(t, base)

	dest := filepath.Join(t.TempDir(), "full")
	if err := CloneRef(filepath.Join(base, "exts"), "", dest); err != nil {
		t.Fatal(err)
	}
	if IsSparse(dest) {
		t.Fatal("fresh full clone should not be sparse")
	}
	// No-op on a full clone — must not remove anything.
	if err := SparseAdd(dest, "triage"); err != nil {
		t.Fatalf("SparseAdd on full clone: %v", err)
	}
	mustExist(t, filepath.Join(dest, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(dest, "triage", "tri", "SKILL.md"))
}

func TestSparseAddIsIdempotent(t *testing.T) {
	base := t.TempDir()
	initSubdirRepo(t, base)

	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(filepath.Join(base, "exts"), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := SparseAdd(dest, "triage"); err != nil {
			t.Fatalf("SparseAdd #%d: %v", i, err)
		}
	}
	mustExist(t, filepath.Join(dest, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(dest, "triage", "tri", "SKILL.md"))
}
