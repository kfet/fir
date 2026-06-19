package pkg

import (
	"os"
	"path/filepath"
	"testing"
)

// writePkgFile writes content to path under a package dir, creating parents.
func writePkgFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// TestMakeJSWrapper exercises the `main → run.sh` symlink writer directly.
func TestMakeJSWrapper(t *testing.T) {
	extDir := filepath.Join(t.TempDir(), "pi-llama")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runSh := filepath.Join(t.TempDir(), "run.sh")
	writePkgFile(t, runSh, "#!/usr/bin/env bash\n", 0o755)

	created, err := makeJSWrapper(extDir, runSh)
	if err != nil {
		t.Fatalf("makeJSWrapper: %v", err)
	}
	if !created {
		t.Fatal("expected main symlink to be created")
	}

	link := filepath.Join(extDir, "main")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("main symlink not written: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("main should be a symlink, got mode %v", fi.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil || target != runSh {
		t.Errorf("symlink target = %q (err %v), want %q", target, err, runSh)
	}
	// Following the symlink reaches an executable regular file.
	if info, err := os.Stat(link); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("main does not resolve to an executable: info=%v err=%v", info, err)
	}

	// Idempotent: a valid symlink is left untouched.
	created, err = makeJSWrapper(extDir, runSh)
	if err != nil {
		t.Fatalf("makeJSWrapper (2nd): %v", err)
	}
	if created {
		t.Error("expected second makeJSWrapper to be a no-op")
	}
}

// TestMakeJSWrapperRepairsDangling verifies a dangling `main` symlink (target
// gone, e.g. after an SDK-cache change) is repaired on reinstall.
func TestMakeJSWrapperRepairsDangling(t *testing.T) {
	extDir := filepath.Join(t.TempDir(), "pi-llama")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Point main at a non-existent target.
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "run.sh"), filepath.Join(extDir, "main")); err != nil {
		t.Fatal(err)
	}
	runSh := filepath.Join(t.TempDir(), "run.sh")
	writePkgFile(t, runSh, "#!/usr/bin/env bash\n", 0o755)

	created, err := makeJSWrapper(extDir, runSh)
	if err != nil {
		t.Fatalf("makeJSWrapper: %v", err)
	}
	if !created {
		t.Fatal("expected dangling symlink to be repaired (recreated)")
	}
	if target, _ := os.Readlink(filepath.Join(extDir, "main")); target != runSh {
		t.Errorf("repaired symlink target = %q, want %q", target, runSh)
	}
}

// TestFindJSTSEntryDirs verifies entry-point detection: conventional names at
// root and inside extensions/ext, with helpers, shebanged files, and skip
// dirs excluded.
func TestFindJSTSEntryDirs(t *testing.T) {
	root := t.TempDir()
	// Root entry point.
	writePkgFile(t, filepath.Join(root, "index.ts"), "export default function(){}\n", 0o644)
	// A helper module (not an entry point) in the same dir — dir already included.
	writePkgFile(t, filepath.Join(root, "helpers.ts"), "export const x = 1\n", 0o644)
	// An entry directly inside an `extensions/` dir.
	writePkgFile(t, filepath.Join(root, "extensions", "index.ts"), "export default function(){}\n", 0o644)
	// A deeper, non-`extensions`-named subdir — NOT descended into.
	writePkgFile(t, filepath.Join(root, "extensions", "foo", "foo.ts"), "export default function(){}\n", 0o644)
	// A shebanged .js directly under ext/ — already a native extension, skipped.
	writePkgFile(t, filepath.Join(root, "ext", "main.js"), "#!/usr/bin/env node\n", 0o755)
	// A skip dir with an entry — must be ignored.
	writePkgFile(t, filepath.Join(root, "node_modules", "dep", "index.js"), "module.exports={}\n", 0o644)
	// A non-entry dir (src) — not descended into.
	writePkgFile(t, filepath.Join(root, "src", "index.ts"), "export default function(){}\n", 0o644)

	dirs, err := findJSTSEntryDirs(root)
	if err != nil {
		t.Fatalf("findJSTSEntryDirs: %v", err)
	}
	got := map[string]bool{}
	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)
		got[rel] = true
	}
	if !got["."] {
		t.Errorf("root entry not found; got %v", got)
	}
	if !got["extensions"] {
		t.Errorf("extensions/ entry not found; got %v", got)
	}
	if got[filepath.Join("extensions", "foo")] {
		t.Errorf("deep non-extensions subdir should not be descended into; got %v", got)
	}
	if got["ext"] {
		t.Errorf("shebanged native entry should be skipped; got %v", got)
	}
	if got["src"] {
		t.Errorf("src/ should not be descended into; got %v", got)
	}
	if got[filepath.Join("node_modules", "dep")] {
		t.Errorf("node_modules should be skipped; got %v", got)
	}
}

// TestDirEntryPoint verifies the extensionless entry-point detection used by
// package auto-discovery to honour the `main`/binary convention.
func TestDirEntryPoint(t *testing.T) {
	t.Run("main symlink", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "pi-llama")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runSh := filepath.Join(t.TempDir(), "run.sh")
		writePkgFile(t, runSh, "#!/usr/bin/env bash\n", 0o755)
		if err := os.Symlink(runSh, filepath.Join(dir, "main")); err != nil {
			t.Fatal(err)
		}
		if got := dirEntryPoint(dir); got != filepath.Join(dir, "main") {
			t.Errorf("dirEntryPoint = %q, want .../main", got)
		}
	})
	t.Run("binary named after dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "mybin")
		writePkgFile(t, filepath.Join(dir, "mybin"), "\x7fELF binary-ish\n", 0o755)
		if got := dirEntryPoint(dir); got != filepath.Join(dir, "mybin") {
			t.Errorf("dirEntryPoint = %q, want .../mybin", got)
		}
	})
	t.Run("non-executable main ignored", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "x")
		writePkgFile(t, filepath.Join(dir, "main"), "not exec\n", 0o644)
		if got := dirEntryPoint(dir); got != "" {
			t.Errorf("dirEntryPoint = %q, want empty", got)
		}
	})
	t.Run("script main.sh not treated as dir entry", func(t *testing.T) {
		// main.sh has an extension → must go through the .py/.sh file path,
		// not the extensionless dir-entry path.
		dir := filepath.Join(t.TempDir(), "x")
		writePkgFile(t, filepath.Join(dir, "main.sh"), "#!/bin/sh\n", 0o755)
		if got := dirEntryPoint(dir); got != "" {
			t.Errorf("dirEntryPoint = %q, want empty (script, not extensionless)", got)
		}
	})
}

// TestGenerateJSWrappersEndToEnd: a real package dir gets a `main → run.sh`
// symlink that ScanPackageResources collects as an extension entry. Uses the
// real SDK extraction path, doubling as a smoke test that the SDK ships run.sh.
func TestGenerateJSWrappersEndToEnd(t *testing.T) {
	pkgDir := filepath.Join(t.TempDir(), "pi-llama")
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"),
		"import type { ExtensionAPI } from \"@earendil-works/pi-coding-agent\"\nexport default function(pi){}\n", 0o644)

	n, err := GenerateJSWrappers(pkgDir)
	if err != nil {
		t.Fatalf("GenerateJSWrappers: %v", err)
	}
	if n != 1 {
		t.Fatalf("created %d wrappers, want 1", n)
	}

	res, err := ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	found := false
	for _, e := range res.Extensions {
		if e == filepath.Join(pkgDir, "main") {
			found = true
		}
	}
	if !found {
		t.Fatalf("generated main symlink not discovered; extensions=%v", res.Extensions)
	}
}

// TestAutoDiscoverBinaryExtension proves a compiled binary extension (no
// frontmatter, no install hook) shipped in a package is discovered via the
// `main`/`<dirname>` convention.
func TestAutoDiscoverBinaryExtension(t *testing.T) {
	pkgDir := t.TempDir()
	// A binary extension in extensions/widget/ named after its dir.
	writePkgFile(t, filepath.Join(pkgDir, "extensions", "widget", "widget"),
		"\x7fELF not-really\n", 0o755)
	// A README that must not be mistaken for an extension.
	writePkgFile(t, filepath.Join(pkgDir, "README.md"), "# pkg\n", 0o644)

	res, err := ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	want := filepath.Join(pkgDir, "extensions", "widget", "widget")
	found := false
	for _, e := range res.Extensions {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("binary extension not discovered; extensions=%v", res.Extensions)
	}
}

// TestDanglingMainSymlinkSelfHeals proves a `main → run.sh` symlink whose SDK
// target was pruned is re-pointed to the current SDK's run.sh during discovery,
// so the extension keeps loading across SDK-cache changes. Uses the real SDK
// extraction path.
func TestDanglingMainSymlinkSelfHeals(t *testing.T) {
	pkgDir := filepath.Join(t.TempDir(), "pi-llama")
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"), "export default function(pi){}\n", 0o644)
	// A dangling main → <gone>/run.sh, mimicking a pruned SDK cache dir.
	gone := filepath.Join(t.TempDir(), "sdks", "deadbeef", "node", "run.sh")
	if err := os.Symlink(gone, filepath.Join(pkgDir, "main")); err != nil {
		t.Fatal(err)
	}
	// Sanity: it is currently dangling.
	if _, err := os.Stat(filepath.Join(pkgDir, "main")); err == nil {
		t.Fatal("precondition: main should be dangling")
	}

	res, err := ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	main := filepath.Join(pkgDir, "main")
	found := false
	for _, e := range res.Extensions {
		if e == main {
			found = true
		}
	}
	if !found {
		t.Fatalf("dangling main not healed/discovered; extensions=%v", res.Extensions)
	}
	// The symlink now resolves to an executable run.sh.
	info, err := os.Stat(main)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("main did not heal to an executable run.sh: info=%v err=%v", info, err)
	}
	if target, _ := os.Readlink(main); filepath.Base(target) != "run.sh" {
		t.Errorf("healed target = %q, want a run.sh", target)
	}
}
