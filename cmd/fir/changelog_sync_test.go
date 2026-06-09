package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRootChangelogIsSymlink guards the single-source changelog design.
//
// cmd/fir/CHANGELOG.md is the real, release-maintained changelog baked into the
// binary via //go:embed (see changelog_init.go). The repo-root CHANGELOG.md is
// a symlink to it, so there is exactly one source of truth. This broke once:
// the v0.60.1 release materialised the root symlink into a regular file, after
// which the embedded copy silently drifted three releases stale (frozen at
// 0.61.0 while root advanced to 0.64.0). This test fails if root CHANGELOG.md
// is ever a regular file again, or points anywhere other than cmd/fir.
//
// To fix a failure, from the repo root:
//
//	rm CHANGELOG.md && ln -s cmd/fir/CHANGELOG.md CHANGELOG.md
func TestRootChangelogIsSymlink(t *testing.T) {
	const root = "../../CHANGELOG.md" // test cwd is the cmd/fir package dir

	fi, err := os.Lstat(root)
	if err != nil {
		t.Fatalf("lstat %s: %v", root, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("repo-root CHANGELOG.md must be a symlink to cmd/fir/CHANGELOG.md " +
			"(single source baked into the binary), but it is a regular file. " +
			"A release likely materialised it; restore with:\n" +
			"\trm CHANGELOG.md && ln -s cmd/fir/CHANGELOG.md CHANGELOG.md")
	}
	target, err := os.Readlink(root)
	if err != nil {
		t.Fatalf("readlink %s: %v", root, err)
	}
	if filepath.Clean(target) != filepath.Clean("cmd/fir/CHANGELOG.md") {
		t.Fatalf("root CHANGELOG.md points to %q, want cmd/fir/CHANGELOG.md", target)
	}
}
