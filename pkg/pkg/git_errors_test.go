// Error-path coverage for the git plumbing.
//
// Several tests here force a git failure by making a directory unwritable
// (0500). That works as an ordinary user and is how CI runs; running the
// suite as root defeats the permission check and those tests fail. Since
// pkg/pkg is sealed at 100% in .covignore, this is a standing constraint:
// run the test suite unprivileged.
package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit neutralises the developer's global/system git config for the
// production git calls under test, which run with the process environment.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
}

// sparseRepo creates the two-subdirectory bare repo used by the sparse tests
// and returns its local URL.
func sparseRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	initSubdirRepo(t, base)
	return filepath.Join(base, "exts")
}

// TestCloneRejectsFlagLikeURL pins the argument-injection guard: a URL that
// starts with "-" must be refused before it reaches git, where it would be
// parsed as a flag.
func TestCloneRejectsFlagLikeURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Clone", func() error { return Clone("--upload-pack=touch /tmp/pwned", dest) }},
		{"CloneRef", func() error { return CloneRef("--upload-pack=touch /tmp/pwned", "main", dest) }},
		{"SparseCloneRef", func() error { return SparseCloneRef("-x", "", "sub", dest) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("expected a flag-like URL to be rejected")
			}
			if !strings.Contains(err.Error(), "must not start with '-'") {
				t.Errorf("unexpected error: %v", err)
			}
			if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
				t.Errorf("rejected clone must not create %s", dest)
			}
		})
	}
}

// TestSparseCloneRefRejectsFlagLikeSubDir covers the second guard in
// SparseCloneRef: the subdirectory is passed to "sparse-checkout set".
func TestSparseCloneRefRejectsFlagLikeSubDir(t *testing.T) {
	err := SparseCloneRef("https://example.invalid/x/y", "", "--force", filepath.Join(t.TempDir(), "dest"))
	if err == nil {
		t.Fatal("expected a flag-like subdirectory to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid subdirectory") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCheckoutRefRejectsFlagLikeRef covers the same guard for refs.
func TestCheckoutRefRejectsFlagLikeRef(t *testing.T) {
	err := CheckoutRef(t.TempDir(), "-f")
	if err == nil {
		t.Fatal("expected a flag-like ref to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid ref") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCloneFailureReportsStderr pins that a failed clone surfaces git's own
// diagnostics — without them the user gets an opaque "exit status 128".
func TestCloneFailureReportsStderr(t *testing.T) {
	isolateGit(t)
	missing := filepath.Join(t.TempDir(), "no-such-repo")
	dest := filepath.Join(t.TempDir(), "dest")

	err := Clone(missing, dest)
	if err == nil {
		t.Fatal("expected cloning a non-existent repository to fail")
	}
	if !strings.Contains(err.Error(), "git clone "+missing) {
		t.Errorf("error should name the failing command and repo, got: %v", err)
	}
	if !strings.Contains(err.Error(), "fatal:") {
		t.Errorf("error should carry git's own stderr, got: %v", err)
	}
}

// TestCloneRefAtBranch covers the --branch arm: CloneRef with a ref checks
// that ref out, and a ref that does not exist is a hard error.
func TestCloneRefAtBranch(t *testing.T) {
	isolateGit(t)
	o := newOrigin(t)

	dest := filepath.Join(t.TempDir(), "at-other")
	if err := CloneRef(o.url, "other", dest); err != nil {
		t.Fatalf("CloneRef at branch: %v", err)
	}
	if got := gitIn(t, dest, "symbolic-ref", "--short", "HEAD"); got != "other" {
		t.Fatalf("HEAD on %q, want other", got)
	}
	mustExist(t, filepath.Join(dest, "g"))

	err := CloneRef(o.url, "nope", filepath.Join(t.TempDir(), "bad-ref"))
	if err == nil {
		t.Fatal("expected CloneRef at a non-existent ref to fail")
	}
	if !strings.Contains(err.Error(), "@nope") {
		t.Errorf("error should name the ref, got: %v", err)
	}
}

// TestSparseCloneRefCleansUpOnCheckoutFailure pins the cleanup contract: when
// the clone succeeds but the sparse-checkout step fails, no half-built clone
// is left behind for the next run to mistake for a good one.
func TestSparseCloneRefCleansUpOnCheckoutFailure(t *testing.T) {
	isolateGit(t)
	url := sparseRepo(t)
	dest := filepath.Join(t.TempDir(), "dest")

	// Cone mode rejects glob metacharacters, so the clone lands and the
	// "sparse-checkout set" step is what fails.
	err := SparseCloneRef(url, "", "*", dest)
	if err == nil {
		t.Fatal("expected sparse checkout of a pattern to fail")
	}
	if !strings.Contains(err.Error(), "sparse checkout") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("failed sparse clone must not leave %s behind", dest)
	}
}

// TestSparseCloneRefFailedCloneReportsStderr covers the clone-step failure of
// the sparse path.
func TestSparseCloneRefFailedCloneReportsStderr(t *testing.T) {
	isolateGit(t)
	missing := filepath.Join(t.TempDir(), "no-such-repo")
	err := SparseCloneRef(missing, "", "sub", filepath.Join(t.TempDir(), "dest"))
	if err == nil {
		t.Fatal("expected sparse clone of a non-existent repository to fail")
	}
	if !strings.Contains(err.Error(), "git clone (sparse)") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSparseAddRejectsFlagLikeSubDir pins that the guard fires only where it
// matters: on a sparse clone, where the value reaches git.
func TestSparseAddRejectsFlagLikeSubDir(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	if err := SparseAdd(dest, "-x"); err == nil || !strings.Contains(err.Error(), "invalid subdirectory") {
		t.Fatalf("want an invalid-subdirectory error, got %v", err)
	}
}

// TestSparseAddPropagatesGitFailure pins that a rejected sparse-checkout
// update is reported rather than swallowed — a silent failure would leave the
// package's files absent with a "successful" install.
func TestSparseAddPropagatesGitFailure(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	err := SparseAdd(dest, "*")
	if err == nil {
		t.Fatal("expected sparse-checkout add of a pattern to fail")
	}
	if !strings.Contains(err.Error(), "sparse-checkout add") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSparseAddEmptySubDirIsNoOp pins the early return for a whole-repo
// package.
func TestSparseAddEmptySubDirIsNoOp(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	if err := SparseAdd(dest, ""); err != nil {
		t.Fatalf("SparseAdd with no subdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "triage")); !os.IsNotExist(err) {
		t.Errorf("empty subdir must not widen the sparse set, err=%v", err)
	}
}

// TestSparseSet pins the shrink path used when uninstalling one of several
// subdirectory packages sharing a clone.
func TestSparseSet(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	if err := SparseAdd(dest, "triage"); err != nil {
		t.Fatal(err)
	}

	// No-op cases: an empty set must not blank the working tree.
	if err := SparseSet(dest, nil); err != nil {
		t.Fatalf("SparseSet(nil): %v", err)
	}
	mustExist(t, filepath.Join(dest, "reminders", "rem", "SKILL.md"))

	// A flag-like entry is refused before any of the set is applied.
	if err := SparseSet(dest, []string{"reminders", "-x"}); err == nil ||
		!strings.Contains(err.Error(), "invalid subdirectory") {
		t.Fatalf("want an invalid-subdirectory error, got %v", err)
	}
	mustExist(t, filepath.Join(dest, "triage", "tri", "SKILL.md"))

	// The real shrink.
	if err := SparseSet(dest, []string{"reminders"}); err != nil {
		t.Fatalf("SparseSet: %v", err)
	}
	mustExist(t, filepath.Join(dest, "reminders", "rem", "SKILL.md"))
	if _, err := os.Stat(filepath.Join(dest, "triage")); !os.IsNotExist(err) {
		t.Errorf("triage should have been pruned, err=%v", err)
	}
}

// TestSparseSetOnFullCloneIsNoOp pins that a non-sparse clone is never
// narrowed — doing so would delete files the user's package needs.
func TestSparseSetOnFullCloneIsNoOp(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "full")
	if err := CloneRef(sparseRepo(t), "", dest); err != nil {
		t.Fatal(err)
	}
	if err := SparseSet(dest, []string{"reminders"}); err != nil {
		t.Fatalf("SparseSet on full clone: %v", err)
	}
	mustExist(t, filepath.Join(dest, "triage", "tri", "SKILL.md"))
}

// TestSparseDisableRestoresFullTree covers the widening path taken when a
// whole-repo package is installed on top of a sparse clone.
func TestSparseDisableRestoresFullTree(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "triage")); !os.IsNotExist(err) {
		t.Fatalf("test setup: triage should be absent from a sparse clone, err=%v", err)
	}

	if err := SparseDisable(dest); err != nil {
		t.Fatalf("SparseDisable: %v", err)
	}
	mustExist(t, filepath.Join(dest, "triage", "tri", "SKILL.md"))
	if IsSparse(dest) {
		t.Error("clone should no longer be sparse")
	}

	// Idempotent: disabling a full clone is a no-op, not an error.
	if err := SparseDisable(dest); err != nil {
		t.Fatalf("second SparseDisable: %v", err)
	}
}

// TestSparseDisablePropagatesGitFailure pins that a failed widening is
// reported: silently continuing would install a package whose files are not
// on disk.
func TestSparseDisablePropagatesGitFailure(t *testing.T) {
	isolateGit(t)
	dest := filepath.Join(t.TempDir(), "sparse")
	if err := SparseCloneRef(sparseRepo(t), "", "reminders", dest); err != nil {
		t.Fatal(err)
	}
	// Read-only working tree: git cannot create the "triage" directory it
	// must materialise when the sparse filter is removed.
	if err := os.Chmod(dest, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o700) })

	err := SparseDisable(dest)
	if err == nil {
		t.Fatal("expected sparse-checkout disable to fail on a read-only tree")
	}
	if !strings.Contains(err.Error(), "sparse-checkout disable") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestIsSparseOnNonRepo pins that a directory git cannot read is reported as
// non-sparse rather than panicking or erroring — callers use it as a
// predicate.
func TestIsSparseOnNonRepo(t *testing.T) {
	isolateGit(t)
	if IsSparse(t.TempDir()) {
		t.Error("a plain directory is not a sparse checkout")
	}
}

// TestPullOnNonRepo pins that Pull surfaces a readable error when HEAD cannot
// be read at all, instead of treating it as a detached head.
func TestPullOnNonRepo(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	err := Pull(dir)
	if err == nil {
		t.Fatal("expected Pull on a non-repository to fail")
	}
	if !strings.Contains(err.Error(), "symbolic-ref") || !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the failing command and directory, got: %v", err)
	}
}

// TestPullNonFastForward pins that a clone with local commits is not
// force-moved: the merge error is reported and HEAD stays put.
func TestPullNonFastForward(t *testing.T) {
	isolateGit(t)
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "--quiet", "-m", "local work")
	before := gitIn(t, clone, "rev-parse", "HEAD")

	o.advance(t, "main", "two")

	err := Pull(clone)
	if err == nil {
		t.Fatal("expected a non-fast-forward pull to fail")
	}
	if !strings.Contains(err.Error(), "merge --ff-only") {
		t.Errorf("error should come from the merge, got: %v", err)
	}
	if got := gitIn(t, clone, "rev-parse", "HEAD"); got != before {
		t.Errorf("failed Pull moved HEAD: %s -> %s", before, got)
	}
}

// TestPullReportsFetchErrorWhenMergeAlsoFails pins the error-precedence rule:
// a lost fetch is only reported when the fast-forward could not stand in for
// it. That ordering is what keeps a racing sibling fetch from turning into a
// spurious failure.
func TestPullReportsFetchErrorWhenMergeAlsoFails(t *testing.T) {
	isolateGit(t)
	o := newOrigin(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if err := Clone(o.url, clone); err != nil {
		t.Fatal(err)
	}
	// Move origin/main ahead while the remote still works, then diverge
	// locally, so the fast-forward genuinely cannot happen.
	o.advance(t, "main", "two")
	if err := Fetch(clone); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "--quiet", "-m", "local work")
	// Now break the fetch.
	gitIn(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	err := Pull(clone)
	if err == nil {
		t.Fatal("expected Pull to fail when both fetch and merge fail")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("the fetch error should win, got: %v", err)
	}
}
