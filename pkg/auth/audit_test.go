package auth

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readAuditLog parses the JSONL audit log next to authPath.
func readAuditLog(t *testing.T, agentDir string) []AuditEntry {
	t.Helper()
	p := filepath.Join(agentDir, auditLogName)
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestAuditLogRecordsSetAndRemove(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(authPath)

	if err := s.Set("anthropic#work", AuthCredential{
		Type:   CredentialTypeOAuth,
		Label:  "Work (acme)",
		Access: "tok",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Remove("anthropic#work"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	entries := readAuditLog(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d audit entries, want 2: %+v", len(entries), entries)
	}

	set := entries[0]
	if set.Action != AuditActionSet || set.Slot != "anthropic#work" {
		t.Errorf("set entry = %+v", set)
	}
	if set.Type != string(CredentialTypeOAuth) || set.Label != "Work (acme)" {
		t.Errorf("set entry missing type/label: %+v", set)
	}
	if set.Remain != 1 {
		t.Errorf("set Remain = %d, want 1", set.Remain)
	}
	if set.PID == 0 {
		t.Errorf("set PID not recorded")
	}

	rm := entries[1]
	if rm.Action != AuditActionRemove || rm.Slot != "anthropic#work" {
		t.Errorf("remove entry = %+v", rm)
	}
	if rm.Remain != 0 {
		t.Errorf("remove Remain = %d, want 0", rm.Remain)
	}
	// The whole point: the audit log must capture *who* deleted the slot.
	if len(rm.Callers) == 0 {
		t.Errorf("remove entry has no caller frames; cannot diagnose deletions")
	}
}

func TestAuditLogPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	s1 := NewAuthStorage(authPath)
	_ = s1.Set("anthropic", AuthCredential{Type: CredentialTypeOAuth, Access: "a"})

	// A second AuthStorage over the same path (simulating another process/
	// session) must append, not truncate, the audit history.
	s2 := NewAuthStorage(authPath)
	_ = s2.Remove("anthropic")

	entries := readAuditLog(t, dir)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (append, not truncate)", len(entries))
	}
	if entries[0].Action != AuditActionSet || entries[1].Action != AuditActionRemove {
		t.Errorf("unexpected order: %+v", entries)
	}
}

func TestInMemoryStorageHasNoAudit(t *testing.T) {
	// In-memory storage must not panic and keeps no audit file.
	s := NewInMemoryAuthStorage(nil)
	if err := s.Set("anthropic", AuthCredential{Type: CredentialTypeAPIKey, Key: "k"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Remove("anthropic"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.audit != nil {
		t.Errorf("in-memory storage should have nil audit writer")
	}
}
