// Error-path coverage for JS/TS wrapper generation and symlink healing.
//
// Several tests here force a filesystem failure by making a directory
// unreadable (0000) or unwritable (0500). That works as an ordinary user and
// is how CI runs; running the suite as root defeats the permission check and
// those tests fail. Since pkg/pkg is sealed at 100% in .covignore, this is a
// standing constraint: run the test suite unprivileged. See the header of
// git_errors_test.go, which carries the same constraint for the git plumbing.
//
// The paths exercised here are the ones a happy-path test can never reach and
// that a review of this code found two merge-blocking bugs in: the atomic
// symlink replace, and the guard that decides whether a `main → run.sh`
// symlink belongs to fir or to the user.
package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unwritableCacheRoot points the SDK cache at a directory fir cannot create,
// so sdk.EnsureExtracted — and therefore jsRunShPath and sdkCacheRoot — fails.
// This is the "no JS/TS runtime available" condition in production: a cache
// directory that is read-only, full, or on a filesystem that rejects the write.
func unwritableCacheRoot(t *testing.T) {
	t.Helper()
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	t.Setenv("XDG_CACHE_HOME", filepath.Join(locked, "cache"))
}

// TestJSRunShPathSDKUnavailable pins the contract that an unavailable SDK is
// NOT an install failure. A package that ships JS/TS must still install (its
// skills and themes are unaffected); it simply contributes no wrapper, and so
// no extension, until the runtime can be extracted.
func TestJSRunShPathSDKUnavailable(t *testing.T) {
	unwritableCacheRoot(t)

	if _, err := jsRunShPath(); err == nil {
		t.Fatal("jsRunShPath: want an error when the SDK cannot be extracted")
	}

	pkgDir := t.TempDir()
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"), "export default function(){}\n", 0o644)

	n, err := GenerateJSWrappers(pkgDir)
	if err != nil {
		t.Errorf("GenerateJSWrappers: want nil error when no SDK is available, got %v", err)
	}
	if n != 0 {
		t.Errorf("created = %d, want 0", n)
	}
	if _, err := os.Lstat(filepath.Join(pkgDir, "main")); !os.IsNotExist(err) {
		t.Error("no wrapper should have been created without a run.sh to point at")
	}
}

// TestJSRunShPathMissingRunSh covers the second failure mode of resolving the
// runtime: extraction succeeds but the tree does not contain node/run.sh — a
// cache directory that was partially pruned. Returning the path anyway would
// produce symlinks that dangle from birth.
func TestJSRunShPathMissingRunSh(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	runSh, err := jsRunShPath()
	if err != nil {
		t.Fatalf("jsRunShPath: %v", err)
	}
	if err := os.Remove(runSh); err != nil {
		t.Fatal(err)
	}

	if _, err := jsRunShPath(); err == nil {
		t.Fatal("jsRunShPath: want an error when run.sh is missing from the extracted SDK")
	}
}

// TestGenerateJSWrappersUnwritableEntryDir covers the error propagation out of
// the generation loop. A directory fir can read (so the entry point is found)
// but not write (so the symlink cannot be created) must surface as an error
// rather than silently reporting zero wrappers — the caller turns it into a
// visible warning.
func TestGenerateJSWrappersUnwritableEntryDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	pkgDir := t.TempDir()
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"), "export default function(){}\n", 0o644)
	if err := os.Chmod(pkgDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	n, err := GenerateJSWrappers(pkgDir)
	if err == nil {
		t.Fatal("GenerateJSWrappers: want an error for an unwritable entry directory")
	}
	if n != 0 {
		t.Errorf("created = %d, want 0", n)
	}
	if !strings.Contains(err.Error(), "symlinking") {
		t.Errorf("error should name the failed operation; got %v", err)
	}
}

// TestMakeJSWrapperRealFileNotClobbered guards the blast radius of wrapper
// generation: a package that ships its own executable called `main` — a
// compiled binary extension, which is precisely what the `main` convention
// also serves — must not have it replaced by a symlink to run.sh.
func TestMakeJSWrapperRealFileNotClobbered(t *testing.T) {
	extDir := filepath.Join(t.TempDir(), "binary-ext")
	mainPath := filepath.Join(extDir, "main")
	writePkgFile(t, mainPath, "#!/bin/sh\nexit 0\n", 0o755)

	runSh := filepath.Join(t.TempDir(), "run.sh")
	writePkgFile(t, runSh, "#!/bin/sh\nexit 0\n", 0o755)

	created, err := makeJSWrapper(extDir, runSh)
	if err != nil {
		t.Fatalf("makeJSWrapper: %v", err)
	}
	if created {
		t.Error("created = true, want false — an existing real `main` is left alone")
	}
	if fi, err := os.Lstat(mainPath); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("real `main` was replaced: mode=%v err=%v", fi.Mode(), err)
	}
	got, err := os.ReadFile(mainPath)
	if err != nil || !strings.HasPrefix(string(got), "#!/bin/sh") {
		t.Errorf("real `main` content was altered: %q (err %v)", got, err)
	}
}

// TestFindJSTSEntryDirsSkipsUnreadable pins the deliberate no-error contract of
// the scan: an unreadable directory is skipped at every level of the walk
// (root descent, the extensions/ sub-walk, and the recursion beneath it), and
// the readable siblings are still wrapped. One bad subdirectory must not cost
// a package its other extensions.
func TestFindJSTSEntryDirsSkipsUnreadable(t *testing.T) {
	root := t.TempDir()
	writePkgFile(t, filepath.Join(root, "index.ts"), "export default function(){}\n", 0o644)
	writePkgFile(t, filepath.Join(root, "extensions", "good", "index.ts"), "export default function(){}\n", 0o644)

	// Unreadable directly under extensions/ — hit by both the walk and the
	// recursive descent beneath extensions/.
	lockedExt := filepath.Join(root, "extensions", "locked")
	writePkgFile(t, filepath.Join(lockedExt, "index.ts"), "export default function(){}\n", 0o644)
	// Unreadable in the ordinary root descent, which is looking for an
	// extensions/ dir it will now never see.
	lockedSrc := filepath.Join(root, "src")
	writePkgFile(t, filepath.Join(lockedSrc, "index.ts"), "export default function(){}\n", 0o644)

	for _, d := range []string{lockedExt, lockedSrc} {
		if err := os.Chmod(d, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
	}

	got := map[string]bool{}
	for _, d := range findJSTSEntryDirs(root) {
		rel, _ := filepath.Rel(root, d)
		got[rel] = true
	}
	if !got["."] {
		t.Errorf("root entry lost to an unreadable sibling; got %v", got)
	}
	if !got[filepath.Join("extensions", "good")] {
		t.Errorf("readable extension lost to an unreadable sibling; got %v", got)
	}
	if got[filepath.Join("extensions", "locked")] || got["src"] {
		t.Errorf("unreadable dirs should contribute nothing; got %v", got)
	}
}

// TestFindJSTSEntryDirsSkipsDeclarationFile covers the `.d.ts` guard. A
// TypeScript declaration file is not executable code, so a package whose only
// `index`-stemmed file is `index.d.ts` must not get a wrapper — fir would spawn
// a type stub as an extension and the handshake would hang.
func TestFindJSTSEntryDirsSkipsDeclarationFile(t *testing.T) {
	root := t.TempDir()
	writePkgFile(t, filepath.Join(root, "index.d.ts"), "export type X = 1\n", 0o644)

	if dirs := findJSTSEntryDirs(root); len(dirs) != 0 {
		t.Errorf("index.d.ts must not qualify as an entry point; got %v", dirs)
	}
}

// TestFindJSTSEntryDirsUnreadableEntryFile covers the shebang probe's open
// failure. A file fir cannot read is treated as having no shebang — i.e. as a
// plain JS/TS entry point needing a wrapper — which is the safe default: the
// alternative silently drops an extension because of a transient read error.
func TestFindJSTSEntryDirsUnreadableEntryFile(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "index.ts")
	writePkgFile(t, entry, "export default function(){}\n", 0o644)
	if err := os.Chmod(entry, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(entry, 0o644) })

	if fileHasShebang(entry) {
		t.Error("an unreadable file must not be reported as shebanged")
	}
	dirs := findJSTSEntryDirs(root)
	if len(dirs) != 1 || dirs[0] != root {
		t.Errorf("unreadable entry point should still be wrapped; got %v", dirs)
	}
}

// TestReplaceSymlinkRenameFailure covers the rename arm of the atomic replace.
// The temporary link is created successfully and the rename then fails; the
// property that matters is that the temp link is NOT left behind — a scan runs
// on every session start, so a leaked `main.tmp-<pid>` per attempt would
// accumulate in the user's package directory forever.
func TestReplaceSymlinkRenameFailure(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "main")
	// A directory cannot be replaced by renaming a symlink over it.
	if err := os.Mkdir(link, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "run.sh")
	writePkgFile(t, target, "#!/bin/sh\n", 0o755)

	if err := replaceSymlink(link, target); err == nil {
		t.Fatal("replaceSymlink: want an error when the destination is a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "main.tmp-") {
			t.Errorf("temporary link %q leaked after a failed rename", e.Name())
		}
	}
	// The pre-existing entry is untouched.
	if fi, err := os.Lstat(link); err != nil || !fi.IsDir() {
		t.Errorf("destination was damaged by the failed replace: %v (err %v)", fi, err)
	}
}

// TestHealRunShSymlinkForeignTargetName covers the basename guard: a `main`
// symlink that does not point at a file called run.sh is not one of fir's
// runtime wrappers at all, so healing must decline it outright rather than
// re-point it.
func TestHealRunShSymlinkForeignTargetName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "launcher.sh")
	writePkgFile(t, target, "#!/bin/sh\nexit 0\n", 0o755)
	link := filepath.Join(dir, "main")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if healRunShSymlink(link) {
		t.Error("healRunShSymlink should decline a symlink that is not a run.sh wrapper")
	}
	if got, _ := os.Readlink(link); got != target {
		t.Errorf("foreign symlink was rewritten: %q, want %q", got, target)
	}
}

// TestHealRunShSymlinkSDKUnavailable covers both arms that depend on resolving
// the SDK cache while it is unavailable:
//
//   - a DANGLING wrapper cannot be repaired, and healing must report that
//     honestly (false) so discovery does not offer a broken entry point;
//   - a RESOLVING wrapper must be left alone rather than treated as foreign
//     and rewritten — with no cache root to compare against, the only safe
//     answer is "not mine to touch".
func TestHealRunShSymlinkSDKUnavailable(t *testing.T) {
	unwritableCacheRoot(t)

	dir := t.TempDir()
	dangling := filepath.Join(dir, "main")
	if err := os.Symlink(filepath.Join(dir, "gone", "run.sh"), dangling); err != nil {
		t.Fatal(err)
	}
	if healRunShSymlink(dangling) {
		t.Error("a dangling wrapper cannot be healed without an SDK; want false")
	}

	other := t.TempDir()
	target := filepath.Join(other, "run.sh")
	writePkgFile(t, target, "#!/bin/sh\n", 0o755)
	resolving := filepath.Join(other, "main")
	if err := os.Symlink(target, resolving); err != nil {
		t.Fatal(err)
	}
	if !healRunShSymlink(resolving) {
		t.Error("a resolving wrapper is usable; want true")
	}
	if got, _ := os.Readlink(resolving); got != target {
		t.Errorf("resolving symlink was rewritten with no cache root to justify it: %q", got)
	}
	if isSDKCachePath(target) {
		t.Error("isSDKCachePath must answer false when the cache root is unresolvable")
	}
}

// TestHealRunShSymlinkUnwritableDir covers the failed-repair arm: the wrapper
// is genuinely stale and fir has a current run.sh to point it at, but the
// package directory is read-only. Healing must report the link's ACTUAL
// usability (it still resolves, so the extension keeps loading against the old
// SDK) rather than claiming a repair it did not perform.
func TestHealRunShSymlinkUnwritableDir(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	current, err := jsRunShPath()
	if err != nil {
		t.Fatalf("jsRunShPath: %v", err)
	}
	// A superseded SDK tree beside the current one, inside the real cache.
	oldRunSh := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(current))), "0ldc0de", "node", "run.sh")
	writePkgFile(t, oldRunSh, "#!/bin/sh\nexit 0\n", 0o755)

	pkgDir := filepath.Join(t.TempDir(), "pi-llama")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pkgDir, "main")
	if err := os.Symlink(oldRunSh, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pkgDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	if !healRunShSymlink(link) {
		t.Error("a stale-but-resolving wrapper is still usable; want true")
	}
	if got, _ := os.Readlink(link); got != oldRunSh {
		t.Errorf("symlink changed despite an unwritable directory: %q, want %q", got, oldRunSh)
	}
}
