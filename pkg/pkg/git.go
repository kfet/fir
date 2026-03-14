package pkg

import (
	"bytes"
	"fmt"
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
