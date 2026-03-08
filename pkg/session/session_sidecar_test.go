package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadReexecSidecar(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.json")

	sc := &ReexecSidecar{
		QueueMessages: []string{"do thing 1", "do thing 2"},
		PendingInput:  "partial input",
	}
	if err := WriteReexecSidecar(sessionFile, sc); err != nil {
		t.Fatalf("write: %v", err)
	}

	// File should exist before read.
	path := ReexecSidecarPath(sessionFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sidecar file not found after write: %v", err)
	}

	got, err := ReadReexecSidecar(sessionFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil sidecar")
	}
	if len(got.QueueMessages) != 2 || got.QueueMessages[0] != "do thing 1" {
		t.Errorf("unexpected queue: %v", got.QueueMessages)
	}
	if got.PendingInput != "partial input" {
		t.Errorf("unexpected pending input: %q", got.PendingInput)
	}

	// File should be deleted after read.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("sidecar file should be deleted after read")
	}
}

func TestReadReexecSidecar_Missing(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "nonexistent.json")

	got, err := ReadReexecSidecar(sessionFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing file, got %+v", got)
	}
}
