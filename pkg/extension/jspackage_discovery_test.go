package extension_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/extension"
	pkgpkg "github.com/kfet/fir/pkg/pkg"
)

// writeFile writes content to path, creating parent dirs, with mode.
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// TestJSPackageContributesLoadableExtension proves that an installed JS/TS
// package whose post-install step produced a `main → run.sh` symlink flows all
// the way through to a loadable extension config, named after the package
// directory — the same extensionless entry-point convention as
// .fir/extensions/<name>/main:
//
//	ScanPackageResources (auto-discovery) -> GetPackageExtensionPaths
//	  -> ConfigsFromFiles -> ExtProcConfig
func TestJSPackageContributesLoadableExtension(t *testing.T) {
	pkgDir := filepath.Join(t.TempDir(), "pi-llama")

	// A JS/TS entry point (no shebang) — the actual extension source.
	writeFile(t, filepath.Join(pkgDir, "index.ts"),
		"export default function (pi) {}\n", 0o644)

	// The runtime wrapper: a `main → run.sh` symlink (what GenerateJSWrappers
	// produces). run.sh has no comment frontmatter, so the extension is named
	// after its directory.
	runSh := filepath.Join(t.TempDir(), "run.sh")
	writeFile(t, runSh, "#!/usr/bin/env bash\necho hi\n", 0o755)
	if err := os.Symlink(runSh, filepath.Join(pkgDir, "main")); err != nil {
		t.Fatal(err)
	}

	// 1. Auto-discovery must collect the `main` entry point.
	res, err := pkgpkg.ScanPackageResources(pkgDir)
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
		t.Fatalf("main entry not discovered; extensions=%v", res.Extensions)
	}

	// 2. ConfigsFromFiles must turn it into a loadable config named after the
	//    package directory (frontmatter-free executable convention).
	configs := extension.ConfigsFromFiles(res.Extensions)
	var cfg *extension.ExtProcConfig
	for i := range configs {
		if configs[i].Name == "pi-llama" {
			cfg = &configs[i]
		}
	}
	if cfg == nil {
		t.Fatalf("ConfigsFromFiles produced no config named 'pi-llama'; got %+v", configs)
	}
	if cfg.Scope != "package" {
		t.Errorf("scope = %q, want %q", cfg.Scope, "package")
	}
	if cfg.Path != filepath.Join(pkgDir, "main") {
		t.Errorf("path = %q, want .../main", cfg.Path)
	}
}

// TestBinaryPackageExtensionLoadable proves a compiled binary extension (no
// frontmatter) shipped inside a package's extensions/<name>/ dir is discovered
// and named after its directory — the convention generalised from
// .fir/extensions/ to installed packages.
func TestBinaryPackageExtensionLoadable(t *testing.T) {
	pkgDir := t.TempDir()
	bin := filepath.Join(pkgDir, "extensions", "widget", "main")
	writeFile(t, bin, "\x7fELF not-really-a-binary\n", 0o755)

	res, err := pkgpkg.ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	configs := extension.ConfigsFromFiles(res.Extensions)
	var cfg *extension.ExtProcConfig
	for i := range configs {
		if configs[i].Name == "widget" {
			cfg = &configs[i]
		}
	}
	if cfg == nil {
		t.Fatalf("binary extension not loadable; got %+v", configs)
	}
	if cfg.Path != bin {
		t.Errorf("path = %q, want %q", cfg.Path, bin)
	}
}

// TestPyHelperWithoutFrontmatterNotLoaded guards that the relaxed naming rule
// does NOT turn a frontmatter-less .py/.sh helper module into an extension —
// only extensionless executables (binaries / `main` wrappers) get the
// name-from-directory treatment.
func TestPyHelperWithoutFrontmatterNotLoaded(t *testing.T) {
	pkgDir := t.TempDir()
	// A helper script with NO frontmatter.
	writeFile(t, filepath.Join(pkgDir, "helper.py"), "print('helper')\n", 0o755)

	res, err := pkgpkg.ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got := len(extension.ConfigsFromFiles(res.Extensions)); got != 0 {
		t.Fatalf("frontmatter-less .py helper yielded %d configs; want 0", got)
	}
}
