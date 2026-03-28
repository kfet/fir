package acp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSessionFileByUUID(t *testing.T) {
	dir := t.TempDir()

	// Create some session files.
	uuid1 := "813e2575-99b5-4ad3-a0fa-de143ca0ec9b"
	file1 := "2026-03-28T07-00-00Z_" + uuid1 + ".jsonl"
	os.WriteFile(filepath.Join(dir, file1), nil, 0644)
	os.WriteFile(filepath.Join(dir, file1+".meta.json"), nil, 0644)
	os.WriteFile(filepath.Join(dir, "other.jsonl"), nil, 0644)

	// Should find by UUID.
	got := findSessionFileByUUID(dir, uuid1)
	want := filepath.Join(dir, file1)
	if got != want {
		t.Errorf("findSessionFileByUUID = %q, want %q", got, want)
	}

	// Should not find non-existent UUID.
	got = findSessionFileByUUID(dir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if got != "" {
		t.Errorf("expected empty for missing UUID, got %q", got)
	}

	// Should return empty for non-existent directory.
	got = findSessionFileByUUID("/nonexistent", uuid1)
	if got != "" {
		t.Errorf("expected empty for bad dir, got %q", got)
	}
}

func TestResolveSessionByUUID(t *testing.T) {
	// Create a fake agent dir structure.
	agentDir := t.TempDir()
	cwd := "/Users/test/project"
	sessionsDir := filepath.Join(agentDir, "sessions", "--Users-test-project--")
	os.MkdirAll(sessionsDir, 0755)

	uuid := "abcd1234-5678-9abc-def0-123456789abc"
	sessionFile := "2026-01-01T00-00-00Z_" + uuid + ".jsonl"
	os.WriteFile(filepath.Join(sessionsDir, sessionFile), nil, 0644)

	// Set FIR_AGENT_DIR to avoid scanning legacy dirs.
	t.Setenv("FIR_AGENT_DIR", agentDir)

	got := resolveSessionByUUID(uuid, agentDir, cwd)
	want := filepath.Join(sessionsDir, sessionFile)
	if got != want {
		t.Errorf("resolveSessionByUUID = %q, want %q", got, want)
	}

	// Unknown UUID returns empty.
	got = resolveSessionByUUID("00000000-0000-0000-0000-000000000000", agentDir, cwd)
	if got != "" {
		t.Errorf("expected empty for unknown UUID, got %q", got)
	}
}

func TestIsValidSessionPath(t *testing.T) {
	agentDir := t.TempDir()
	sessionsDir := filepath.Join(agentDir, "sessions")
	os.MkdirAll(sessionsDir, 0755)

	t.Setenv("FIR_AGENT_DIR", agentDir)

	validPath := filepath.Join(sessionsDir, "project", "session.jsonl")
	if !isValidSessionPath(validPath, agentDir) {
		t.Error("expected valid path within sessions dir")
	}

	invalidPath := "/tmp/evil/session.jsonl"
	if isValidSessionPath(invalidPath, agentDir) {
		t.Error("expected invalid path outside sessions dir")
	}
}
