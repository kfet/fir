package pkg

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Clone clones url into dest using "git clone".
// The `--` separator is used to prevent URLs that start with `-` from being
// interpreted as flags by git.
func Clone(url, dest string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("invalid git URL %q: must not start with '-'", url)
	}
	cmd := exec.Command("git", "clone", "--", url, dest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", url, err, stderr.String())
	}
	return nil
}

// CloneRef clones url at the given ref (branch/tag/commit) into dest.
// When ref is empty it behaves identically to Clone.
func CloneRef(url, ref, dest string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("invalid git URL %q: must not start with '-'", url)
	}
	args := []string{"clone"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", url, dest)
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s@%s: %w\n%s", url, ref, err, stderr.String())
	}
	return nil
}

// Pull fast-forwards the git repository at dir.
func Pull(dir string) error {
	cmd := exec.Command("git", "-C", dir, "pull", "--ff-only")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git pull in %s: %w\n%s", dir, err, stderr.String())
	}
	return nil
}

// SparseCloneRef clones only a subdirectory of a repo using sparse checkout.
// The full repo metadata is fetched but only the files under subDir are checked out.
// When ref is empty the default branch is used.
func SparseCloneRef(url, ref, subDir, dest string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("invalid git URL %q: must not start with '-'", url)
	}
	if strings.HasPrefix(subDir, "-") {
		return fmt.Errorf("invalid subdirectory %q: must not start with '-'", subDir)
	}

	// 1. git clone --no-checkout [--branch ref] -- url dest
	args := []string{"clone", "--no-checkout", "--filter=blob:none"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", url, dest)
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone (sparse) %s: %w\n%s", url, err, stderr.String())
	}

	// 2. Enable sparse checkout with cone mode
	cmds := [][]string{
		{"git", "-C", dest, "sparse-checkout", "init", "--cone"},
		{"git", "-C", dest, "sparse-checkout", "set", subDir},
		{"git", "-C", dest, "checkout"},
	}
	for _, c := range cmds {
		cmd = exec.Command(c[0], c[1:]...)
		stderr.Reset()
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// Clean up on failure
			_ = os.RemoveAll(dest)
			return fmt.Errorf("sparse checkout %s/%s: %w\n%s", url, subDir, err, stderr.String())
		}
	}
	return nil
}

// Fetch updates remote-tracking refs and tags for the repository at dir.
func Fetch(dir string) error {
	return runGit(dir, "fetch", "--tags", "origin")
}

// CheckoutRef checks out ref (branch, tag, or commit) in the repository at dir.
func CheckoutRef(dir, ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid ref %q: must not start with '-'", ref)
	}
	return runGit(dir, "checkout", "--force", ref)
}

// IsSparse reports whether the repository at dir has sparse checkout enabled.
func IsSparse(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "config", "--bool", "core.sparseCheckout")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// SparseAdd makes subDir available in an existing clone at dir.
// It is idempotent and safe to call on a non-sparse (full) clone, where it is
// a no-op because every path is already checked out.
func SparseAdd(dir, subDir string) error {
	if subDir == "" || !IsSparse(dir) {
		return nil
	}
	if strings.HasPrefix(subDir, "-") {
		return fmt.Errorf("invalid subdirectory %q: must not start with '-'", subDir)
	}
	if err := runGit(dir, "sparse-checkout", "add", subDir); err != nil {
		return err
	}
	// "sparse-checkout add" updates the working tree itself, but a clone made
	// with --no-checkout may still have no HEAD checked out.
	return runGit(dir, "checkout")
}

// SparseSet restricts an existing sparse clone at dir to exactly subDirs.
// It is a no-op on a non-sparse clone or when subDirs is empty.
func SparseSet(dir string, subDirs []string) error {
	if len(subDirs) == 0 || !IsSparse(dir) {
		return nil
	}
	for _, s := range subDirs {
		if strings.HasPrefix(s, "-") {
			return fmt.Errorf("invalid subdirectory %q: must not start with '-'", s)
		}
	}
	return runGit(dir, append([]string{"sparse-checkout", "set"}, subDirs...)...)
}

// SparseDisable turns off sparse checkout at dir, restoring a full working
// tree. It is a no-op when the clone is not sparse.
func SparseDisable(dir string) error {
	if !IsSparse(dir) {
		return nil
	}
	if err := runGit(dir, "sparse-checkout", "disable"); err != nil {
		return err
	}
	return runGit(dir, "checkout")
}

// runGit runs a git command in dir, wrapping failures with stderr output.
func runGit(dir string, args ...string) error {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s in %s: %w\n%s", strings.Join(args, " "), dir, err, stderr.String())
	}
	return nil
}

// CurrentRef returns the short HEAD commit hash of the repository at dir.
func CurrentRef(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse in %s: %w\n%s", dir, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
