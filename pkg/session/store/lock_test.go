//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockSession(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "test-session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	// First lock should succeed.
	lock1, ok := tryLockSession(sessionPath)
	if !ok {
		t.Fatal("expected first tryLockSession to succeed")
	}

	// Second lock on same file should fail (already locked).
	_, ok2 := tryLockSession(sessionPath)
	if ok2 {
		t.Fatal("expected second tryLockSession to fail while first is held")
	}

	// Release first lock; now a new lock should succeed.
	if err := lock1.Close(); err != nil {
		t.Fatal(err)
	}
	lock3, ok3 := tryLockSession(sessionPath)
	if !ok3 {
		t.Fatal("expected tryLockSession to succeed after release")
	}
	lock3.Close()
}

func TestSessionLock_Close_Nil(t *testing.T) {
	var l *sessionLock
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil: %v", err)
	}
}

func TestMetaPath(t *testing.T) {
	got := metaPath("/some/path/session.jsonl")
	want := "/some/path/session.jsonl.meta.json"
	if got != want {
		t.Errorf("metaPath = %q, want %q", got, want)
	}
}

func TestSetSessionFile_ForksLockedSession(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	os.MkdirAll(sessionDir, 0755)

	// Create a session file with valid header.
	sessionPath := filepath.Join(sessionDir, "2026-01-01T00-00-00Z_test-uuid.jsonl")
	os.WriteFile(sessionPath, []byte(`{"type":"session","version":1,"id":"test-uuid","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp"}`+"\n"), 0644)

	// Lock the session to simulate another process.
	lock, ok := tryLockSession(sessionPath)
	if !ok {
		t.Fatal("tryLockSession failed")
	}
	defer lock.Close()

	// setSessionFile should fork when the file is locked.
	ss := &SessionStore{
		cwd:        "/tmp",
		sessionDir: sessionDir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	forked := ss.setSessionFile(sessionPath)
	if !forked {
		t.Fatal("expected setSessionFile to fork locked session")
	}
	if ss.GetSessionFile() == sessionPath {
		t.Error("forked session file should differ from original")
	}
	if _, err := os.Stat(ss.GetSessionFile()); err != nil {
		t.Errorf("forked file should exist: %v", err)
	}
}

func TestSetSessionFile_NoForkWhenUnlocked(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	os.MkdirAll(sessionDir, 0755)

	sessionPath := filepath.Join(sessionDir, "2026-01-01T00-00-00Z_test.jsonl")
	os.WriteFile(sessionPath, []byte(`{"type":"session","version":1,"id":"test","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp"}`+"\n"), 0644)

	ss := &SessionStore{
		cwd:        "/tmp",
		sessionDir: sessionDir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	forked := ss.setSessionFile(sessionPath)
	if forked {
		t.Fatal("expected no fork for unlocked session")
	}
}
