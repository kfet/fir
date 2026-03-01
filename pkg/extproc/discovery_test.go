package extproc

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
	if len(configs) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(configs))
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
	// Subdirectory — should be skipped
	os.MkdirAll(filepath.Join(extDir, "subdir"), 0o755)

	configs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
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

// helpers

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
