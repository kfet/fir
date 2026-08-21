package pkg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeFile creates path (and its parents) with the given contents.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// relNames returns paths relative to root, sorted, so assertions can be
// written against the layout rather than temp-dir noise.
func relNames(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", root, p, err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAutoDiscoverClassifies pins the full auto-discovery rule set: what
// counts as an extension, a skill, and a theme, and what is deliberately
// ignored.
func TestAutoDiscoverClassifies(t *testing.T) {
	root := t.TempDir()

	// Extensions: *.py and *.sh at any depth.
	writeFile(t, filepath.Join(root, "hook.py"), "")
	writeFile(t, filepath.Join(root, "tools", "deep", "run.sh"), "")

	// Skills: SKILL.md below the root, and root-level *.md that is not a
	// well-known documentation file.
	writeFile(t, filepath.Join(root, "reminders", "SKILL.md"), "")
	writeFile(t, filepath.Join(root, "notes.md"), "")

	// Not skills: documentation at the root, and a non-SKILL .md nested in a
	// subdirectory.
	writeFile(t, filepath.Join(root, "README.md"), "")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "")
	writeFile(t, filepath.Join(root, "CODE_OF_CONDUCT.md"), "")
	writeFile(t, filepath.Join(root, "docs", "guide.md"), "")

	// Themes: any file named theme.json, plus any *.json under a "themes" dir.
	writeFile(t, filepath.Join(root, "custom", "theme.json"), "{}")
	writeFile(t, filepath.Join(root, "themes", "dark.json"), "{}")

	// Not a theme: a stray .json elsewhere.
	writeFile(t, filepath.Join(root, "data", "values.json"), "{}")

	// Skipped directory trees are not walked at all.
	writeFile(t, filepath.Join(root, ".git", "hooks", "post-commit.sh"), "")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.py"), "")
	writeFile(t, filepath.Join(root, "vendor", "lib", "SKILL.md"), "")

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}

	if got, want := relNames(t, root, res.Extensions), []string{"hook.py", "tools/deep/run.sh"}; !equalStrings(got, want) {
		t.Errorf("Extensions = %v, want %v", got, want)
	}
	if got, want := relNames(t, root, res.Skills), []string{"notes.md", "reminders/SKILL.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
	if got, want := relNames(t, root, res.Themes), []string{"custom/theme.json", "themes/dark.json"}; !equalStrings(got, want) {
		t.Errorf("Themes = %v, want %v", got, want)
	}
}

// TestAutoDiscoverSingleFilePackage pins that scanning a path that is itself a
// file (a package installed from a single local file) treats that file as
// being at the package root — a lone SKILL.md or notes.md is a skill, not
// nothing.
func TestAutoDiscoverSingleFilePackage(t *testing.T) {
	root := t.TempDir()
	skill := writeFile(t, filepath.Join(root, "notes.md"), "# notes")

	res, err := ScanPackageResources(skill)
	if err != nil {
		t.Fatalf("ScanPackageResources on a file: %v", err)
	}
	if len(res.Skills) != 1 || res.Skills[0] != skill {
		t.Errorf("Skills = %v, want [%s]", res.Skills, skill)
	}

	// A root-level documentation file is still excluded, even alone.
	readme := writeFile(t, filepath.Join(root, "README.md"), "")
	res, err = ScanPackageResources(readme)
	if err != nil {
		t.Fatalf("ScanPackageResources on README: %v", err)
	}
	if len(res.Skills) != 0 {
		t.Errorf("README.md must not be a skill, got %v", res.Skills)
	}
}

// TestAutoDiscoverMissingDir covers the walk-error branch: scanning a
// directory that does not exist yields empty results, not an error, because
// callers treat a missing install path as "nothing contributed".
func TestAutoDiscoverMissingDir(t *testing.T) {
	res, err := ScanPackageResources(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("ScanPackageResources on missing dir: %v", err)
	}
	if len(res.Extensions)+len(res.Skills)+len(res.Themes) != 0 {
		t.Errorf("expected no resources, got %+v", res)
	}
}

// TestAutoDiscoverSkipsUnreadableDir covers the per-entry error branch of the
// walk: an unreadable subdirectory is skipped, and the rest of the tree is
// still discovered.
func TestAutoDiscoverSkipsUnreadableDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ok", "SKILL.md"), "")
	locked := filepath.Join(root, "locked")
	writeFile(t, filepath.Join(locked, "SKILL.md"), "")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Skills), []string{"ok/SKILL.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
}

// TestManifestExpansion covers the fir.json path end to end: explicit files,
// globs, and a trailing-slash pattern meaning "everything under this dir".
func TestManifestExpansion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fir.json"), `{
		"extensions": ["ext/*.py"],
		"skills": ["skills/"],
		"themes": ["theme.json"]
	}`)
	writeFile(t, filepath.Join(root, "ext", "a.py"), "")
	writeFile(t, filepath.Join(root, "ext", "b.py"), "")
	writeFile(t, filepath.Join(root, "ext", "notes.txt"), "")
	writeFile(t, filepath.Join(root, "skills", "one", "SKILL.md"), "")
	writeFile(t, filepath.Join(root, "skills", "two.md"), "")
	writeFile(t, filepath.Join(root, "theme.json"), "{}")
	// A directory matching a glob must not be reported as a file.
	if err := os.MkdirAll(filepath.Join(root, "ext", "dir.py"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Extensions), []string{"ext/a.py", "ext/b.py"}; !equalStrings(got, want) {
		t.Errorf("Extensions = %v, want %v", got, want)
	}
	if got, want := relNames(t, root, res.Skills), []string{"skills/one/SKILL.md", "skills/two.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
	if got, want := relNames(t, root, res.Themes), []string{"theme.json"}; !equalStrings(got, want) {
		t.Errorf("Themes = %v, want %v", got, want)
	}
}

// TestManifestOverridesAutoDiscovery pins that a manifest is authoritative:
// files auto-discovery would have picked up are not reported when fir.json
// does not list them.
func TestManifestOverridesAutoDiscovery(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fir.json"), `{"skills": ["listed/SKILL.md"]}`)
	writeFile(t, filepath.Join(root, "listed", "SKILL.md"), "")
	writeFile(t, filepath.Join(root, "unlisted", "SKILL.md"), "")
	writeFile(t, filepath.Join(root, "hook.py"), "")

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Skills), []string{"listed/SKILL.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
	if len(res.Extensions) != 0 {
		t.Errorf("manifest listed no extensions, got %v", relNames(t, root, res.Extensions))
	}
}

// TestManifestInvalidJSON pins that a broken manifest is a hard error rather
// than a silent fallback to auto-discovery — otherwise a typo in fir.json
// would quietly change what a package contributes.
func TestManifestInvalidJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fir.json"), "{not json")
	writeFile(t, filepath.Join(root, "skill", "SKILL.md"), "")

	_, err := ScanPackageResources(root)
	if err == nil {
		t.Fatal("expected an error for malformed fir.json")
	}
	if !strings.Contains(err.Error(), "parsing fir.json") {
		t.Errorf("error should name the manifest, got: %v", err)
	}
}

// TestManifestUnreadableFallsBackToAutoDiscovery covers the read-error branch:
// a fir.json that cannot be read (here: it is a directory) is treated as
// absent.
func TestManifestUnreadableFallsBackToAutoDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "skill", "SKILL.md"), "")

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Skills), []string{"skill/SKILL.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
}

// TestManifestBadGlobPattern pins that a malformed glob is reported per
// resource kind rather than silently expanding to nothing.
func TestManifestBadGlobPattern(t *testing.T) {
	for _, field := range []string{"extensions", "skills", "themes"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "fir.json"), `{"`+field+`": ["[bad"]}`)
			_, err := ScanPackageResources(root)
			if err == nil {
				t.Fatalf("expected an error for a malformed %s glob", field)
			}
			if !strings.Contains(err.Error(), "syntax error in pattern") {
				t.Errorf("want a pattern-syntax error, got: %v", err)
			}
		})
	}
}

// TestManifestSkipsUnstattableMatch covers the stat-error branch of glob
// expansion: a dangling symlink matches the glob but cannot be stat'd, and
// must be skipped instead of aborting the scan.
func TestManifestSkipsUnstattableMatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fir.json"), `{"extensions": ["*.py"]}`)
	writeFile(t, filepath.Join(root, "real.py"), "")
	if err := os.Symlink(filepath.Join(root, "gone.py"), filepath.Join(root, "dangling.py")); err != nil {
		t.Fatal(err)
	}

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Extensions), []string{"real.py"}; !equalStrings(got, want) {
		t.Errorf("Extensions = %v, want %v", got, want)
	}
}

// TestManifestDirPatternSkipsUnreadable covers the walk-error branch of a
// trailing-slash pattern: unreadable entries are skipped, readable ones are
// still collected, and a missing directory is not fatal.
func TestManifestDirPatternSkipsUnreadable(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "fir.json"), `{"skills": ["skills/", "missing/"]}`)
	writeFile(t, filepath.Join(root, "skills", "ok", "SKILL.md"), "")
	locked := filepath.Join(root, "skills", "locked")
	writeFile(t, filepath.Join(locked, "SKILL.md"), "")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	res, err := ScanPackageResources(root)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if got, want := relNames(t, root, res.Skills), []string{"skills/ok/SKILL.md"}; !equalStrings(got, want) {
		t.Errorf("Skills = %v, want %v", got, want)
	}
}
