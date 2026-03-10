package extension

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatchAndReload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-watcher test in short mode")
	}

	// Create a temp project with an extensions directory.
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(nil)
	mgr.ActiveMode = "interactive"

	api := newMockAPI()
	if err := mgr.Start(context.Background(), projectDir, projectDir, api); err != nil {
		t.Fatal(err)
	}

	// Track reload callbacks.
	var mu sync.Mutex
	var reloadCount int

	stop, err := mgr.WatchAndReload(context.Background(), func(err error) {
		mu.Lock()
		reloadCount++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Write a new extension file — should trigger reload.
	extFile := filepath.Join(extDir, "test_ext.py")
	if err := os.WriteFile(extFile, []byte("#!/usr/bin/env python3\n# ---\n# name: test-ext\n# ---\nprint('hello')"), 0755); err != nil {
		t.Fatal(err)
	}

	// Wait for debounced reload (500ms debounce + margin).
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		count := reloadCount
		mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for auto-reload callback")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestWatchAndReload_IgnoresPycache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-watcher test in short mode")
	}

	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(nil)
	mgr.ActiveMode = "interactive"

	api := newMockAPI()
	if err := mgr.Start(context.Background(), projectDir, projectDir, api); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloadCount int

	stop, err := mgr.WatchAndReload(context.Background(), func(err error) {
		mu.Lock()
		reloadCount++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Write to __pycache__ — should NOT trigger reload.
	pycacheDir := filepath.Join(extDir, "__pycache__")
	if err := os.MkdirAll(pycacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(pycacheDir, "test.pyc")
	if err := os.WriteFile(cacheFile, []byte("cached"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait long enough for a debounced reload.
	time.Sleep(1 * time.Second)

	mu.Lock()
	count := reloadCount
	mu.Unlock()
	if count != 0 {
		t.Errorf("expected no reloads for __pycache__ changes, got %d", count)
	}
}

func TestWatchAndReload_StopCancels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-watcher test in short mode")
	}

	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(nil)
	mgr.ActiveMode = "interactive"

	api := newMockAPI()
	if err := mgr.Start(context.Background(), projectDir, projectDir, api); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reloadCount int

	stop, err := mgr.WatchAndReload(context.Background(), func(err error) {
		mu.Lock()
		reloadCount++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	// Stop immediately, then write a file — no reload should happen.
	stop()

	extFile := filepath.Join(extDir, "test_ext.py")
	if err := os.WriteFile(extFile, []byte("#!/usr/bin/env python3\n# ---\n# name: test-ext\n# ---\nprint('hello')"), 0755); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1 * time.Second)

	mu.Lock()
	count := reloadCount
	mu.Unlock()
	if count != 0 {
		t.Errorf("expected no reloads after stop, got %d", count)
	}
}
