package extension

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDiscover_Empty(t *testing.T) {
	dir := t.TempDir()
	configs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Filter out builtins — we only care that no project/global are found.
	configs = filterScope(configs, "builtin")
	if len(configs) != 0 {
		t.Fatalf("expected 0 non-builtin configs, got %d", len(configs))
	}
}

func TestDiscover_ProjectLocal(t *testing.T) {
	dir := t.TempDir()
	extDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Executable file
	writeExec(t, filepath.Join(extDir, "hello.sh"))
	// Non-executable file — should be skipped
	os.WriteFile(filepath.Join(extDir, "skip.txt"), []byte("x"), 0o644)
	// Subdirectory with no executables — should be skipped
	os.MkdirAll(filepath.Join(extDir, "subdir"), 0o755)

	configs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Filter out builtins — we only care about project-local discovery.
	configs = filterScope(configs, "builtin")
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Name != "hello" {
		t.Errorf("expected name 'hello', got %q", configs[0].Name)
	}
	if configs[0].Scope != "project" {
		t.Errorf("expected scope 'project', got %q", configs[0].Scope)
	}
}

func TestDiscover_ProjectShadowsGlobal(t *testing.T) {
	projectDir := t.TempDir()
	globalDir := t.TempDir()

	writeExec(t, filepath.Join(globalDir, "tool"))
	writeExec(t, filepath.Join(projectDir, "tool"))

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Scope != "project" {
		t.Errorf("expected project scope (shadow), got %q", configs[0].Scope)
	}
	if configs[0].Path != filepath.Join(projectDir, "tool") {
		t.Errorf("expected project path, got %q", configs[0].Path)
	}
}

func TestDiscover_MultipleExtensions(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeExec(t, filepath.Join(globalDir, "alpha"))
	writeExec(t, filepath.Join(globalDir, "beta.py"))
	writeExec(t, filepath.Join(projectDir, "gamma.sh"))

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}

	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	if len(configs) != 3 {
		t.Fatalf("expected 3, got %d", len(configs))
	}
	names := []string{configs[0].Name, configs[1].Name, configs[2].Name}
	expected := []string{"alpha", "beta", "gamma"}
	for i, n := range names {
		if n != expected[i] {
			t.Errorf("index %d: expected %q, got %q", i, expected[i], n)
		}
	}
}

func TestDiscoverWithDirs_GlobalOnly(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir() // empty

	writeExec(t, filepath.Join(globalDir, "global-tool.py"))

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Name != "global-tool" {
		t.Errorf("expected name 'global-tool', got %q", configs[0].Name)
	}
	if configs[0].Scope != "global" {
		t.Errorf("expected scope 'global', got %q", configs[0].Scope)
	}
}

func TestDiscoverWithDirs_NonexistentDirs(t *testing.T) {
	configs, err := DiscoverWithDirs("/nonexistent/global", "/nonexistent/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(configs))
	}
}

func TestDiscover_SubdirMainPy(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Sub-directory with main.py — discovered as "myext"
	subdir := filepath.Join(globalDir, "myext")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPy := filepath.Join(subdir, "main.py")
	writeExec(t, mainPy)

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d: %v", len(configs), configs)
	}
	cfg := configs[0]
	if cfg.Name != "myext" {
		t.Errorf("expected name 'myext', got %q", cfg.Name)
	}
	if cfg.Path != mainPy {
		t.Errorf("expected path %q, got %q", mainPy, cfg.Path)
	}
	if cfg.Scope != "global" {
		t.Errorf("expected scope 'global', got %q", cfg.Scope)
	}
}

func TestDiscover_SubdirDirnameEntry(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Sub-directory named "notify" with notify.py inside
	subdir := filepath.Join(projectDir, "notify")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	entryPoint := filepath.Join(subdir, "notify.py")
	writeExec(t, entryPoint)

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Name != "notify" {
		t.Errorf("expected 'notify', got %q", configs[0].Name)
	}
	if configs[0].Path != entryPoint {
		t.Errorf("expected path %q, got %q", entryPoint, configs[0].Path)
	}
}

func TestDiscover_SubdirFallbackFirstExec(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Sub-directory with an oddly-named executable (no standard name)
	subdir := filepath.Join(globalDir, "tool")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(subdir, "run.sh"))
	// Non-executable — should be ignored
	os.WriteFile(filepath.Join(subdir, "README.md"), []byte("x"), 0o644)

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Name != "tool" {
		t.Errorf("expected 'tool', got %q", configs[0].Name)
	}
}

func TestDiscover_SubdirNoExecutable(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Sub-directory with no executable — should be skipped
	subdir := filepath.Join(projectDir, "empty-ext")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(subdir, "README.md"), []byte("x"), 0o644)

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected 0 configs (subdir with no exec skipped), got %d", len(configs))
	}
}

func TestDiscover_SubdirShadowsGlobalFile(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Global has a plain file named "tool"
	writeExec(t, filepath.Join(globalDir, "tool.py"))

	// Project has a sub-directory named "tool" with main.py — should shadow
	subdir := filepath.Join(projectDir, "tool")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(subdir, "main.py"))

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 (project shadows global), got %d", len(configs))
	}
	if configs[0].Scope != "project" {
		t.Errorf("expected project scope, got %q", configs[0].Scope)
	}
}

func TestDiscoverWithDirs_SkipsBuiltinMarked(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	// Write an extension with builtin: true — should be skipped by scanExtDir.
	content := "#!/usr/bin/env python3\n# ---\n# builtin: true\n# ---\nimport sys\n"
	if err := os.WriteFile(filepath.Join(projectDir, "builtin-ext.py"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a normal extension — should be discovered.
	writeExec(t, filepath.Join(projectDir, "normal.sh"))

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config (builtin skipped), got %d: %v", len(configs), configs)
	}
	if configs[0].Name != "normal" {
		t.Errorf("expected 'normal', got %q", configs[0].Name)
	}
}

// helpers

func filterScope(configs []ExtProcConfig, excludeScope string) []ExtProcConfig {
	var out []ExtProcConfig
	for _, c := range configs {
		if c.Scope != excludeScope {
			out = append(out, c)
		}
	}
	return out
}

func TestDiscoverWithDirs_ParsesModesFromFrontmatter(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	script := `#!/usr/bin/env python3
# ---
# modes: tui, acp
# ---
print("ok")
`
	path := filepath.Join(projectDir, "mode-ext.py")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if got := configs[0].Modes; len(got) != 2 || got[0] != "tui" || got[1] != "acp" {
		t.Fatalf("modes = %v, want [tui acp]", got)
	}
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	content := "#!/bin/sh\n# ---\n# name: test\n# ---\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDiscover_SymlinkedSubdir: a subdirectory entry that is a symlink to a
// real extension directory should be discovered. Previously e.IsDir() returned
// false for the symlink (lstat) and the entry was silently dropped.
func TestDiscover_SymlinkedSubdir(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()
	external := t.TempDir()

	realDir := filepath.Join(external, "linkedext")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPy := filepath.Join(realDir, "main.py")
	writeExec(t, mainPy)

	if err := os.Symlink(realDir, filepath.Join(globalDir, "linkedext")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	configs, err := DiscoverWithDirs(globalDir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != "linkedext" {
		t.Fatalf("expected linkedext, got %+v", configs)
	}
	if configs[0].Path != mainPy {
		// Going through the symlink is also valid.
		viaLink := filepath.Join(globalDir, "linkedext", "main.py")
		if configs[0].Path != viaLink {
			t.Errorf("expected path %q or %q, got %q", mainPy, viaLink, configs[0].Path)
		}
	}
}
