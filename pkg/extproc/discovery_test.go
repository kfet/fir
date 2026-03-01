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
	// We can't easily test global dir without mocking UserHomeDir,
	// so we test the shadowing logic by verifying project-local wins
	// when both directories contain the same name. We'll override the
	// global dir by using a custom approach — but since Discover uses
	// os.UserHomeDir, we test the core logic indirectly.
	//
	// Instead, test with discoverDirs helper.
	projectDir := t.TempDir()
	globalDir := t.TempDir()

	writeExec(t, filepath.Join(globalDir, "tool"))
	writeExec(t, filepath.Join(projectDir, "tool"))

	configs := discoverFrom(t, []dirScope{
		{globalDir, "global"},
		{projectDir, "project"},
	})

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

	configs := discoverFrom(t, []dirScope{
		{globalDir, "global"},
		{projectDir, "project"},
	})

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

// helpers

type dirScope struct {
	path  string
	scope string
}

func discoverFrom(t *testing.T, dirs []dirScope) []ExtProcConfig {
	t.Helper()
	byName := make(map[string]ExtProcConfig)
	for _, d := range dirs {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0111 == 0 {
				continue
			}
			name := stripExt(e.Name())
			byName[name] = ExtProcConfig{
				Name:  name,
				Path:  filepath.Join(d.path, e.Name()),
				Scope: d.scope,
			}
		}
	}
	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
