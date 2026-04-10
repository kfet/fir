package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestNewStore_CreatesFile(t *testing.T) {
	dir := tempDir(t)
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "access.json")); err != nil {
		t.Fatalf("access.json not created: %v", err)
	}
	if len(s.AllowFrom()) != 0 {
		t.Errorf("allowFrom: got %v, want empty", s.AllowFrom())
	}
}

func TestNewStore_LoadsExisting(t *testing.T) {
	dir := tempDir(t)
	data := stateFile{
		AllowFrom: []string{"u-existing"},
		Pending:   map[string]PendingEntry{},
	}
	raw, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(dir, "access.json"), raw, 0o600)

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !s.IsAllowed("u-existing") {
		t.Error("u-existing should be allowed")
	}
	if s.IsAllowed("u-other") {
		t.Error("u-other should not be allowed")
	}
}

func TestGenerateCode_And_Pair(t *testing.T) {
	dir := tempDir(t)
	s, _ := NewStore(dir)

	code, err := s.GenerateCode("u-new")
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("code length: got %d, want 6", len(code))
	}
	if s.PendingCount() != 1 {
		t.Errorf("pending: got %d, want 1", s.PendingCount())
	}

	// Same user gets same code.
	code2, _ := s.GenerateCode("u-new")
	if code2 != code {
		t.Errorf("same user got different code: %q vs %q", code, code2)
	}

	// Pair with the code.
	uid, err := s.Pair(code)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if uid != "u-new" {
		t.Errorf("paired uid: got %q", uid)
	}
	if !s.IsAllowed("u-new") {
		t.Error("u-new should be allowed after pairing")
	}
	if s.PendingCount() != 0 {
		t.Errorf("pending after pair: got %d, want 0", s.PendingCount())
	}

	// State survived reload.
	s2, _ := NewStore(dir)
	if !s2.IsAllowed("u-new") {
		t.Error("u-new not persisted to disk")
	}
}

func TestPair_CodeNotFound(t *testing.T) {
	dir := tempDir(t)
	s, _ := NewStore(dir)
	_, err := s.Pair("abcdef")
	if err != ErrCodeNotFound {
		t.Errorf("err: got %v, want ErrCodeNotFound", err)
	}
}

func TestPair_Expired(t *testing.T) {
	dir := tempDir(t)
	s, _ := NewStore(dir)
	// Manually inject an expired code.
	s.mu.Lock()
	s.data.Pending["aaaaaa"] = PendingEntry{
		UserID:    "u-old",
		ExpiresAt: time.Now().Add(-1 * time.Minute),
	}
	s.mu.Unlock()

	_, err := s.Pair("aaaaaa")
	if err != ErrCodeNotFound {
		t.Errorf("err: got %v, want ErrCodeNotFound", err)
	}
}

func TestPair_AlreadyPaired(t *testing.T) {
	dir := tempDir(t)
	s, _ := NewStore(dir)

	code, _ := s.GenerateCode("u-dup")
	s.Pair(code)

	// Generate a new code for the same (now allowed) user and try pairing again.
	code2, _ := s.GenerateCode("u-dup")
	// GenerateCode returns a new code because the old one was consumed.
	_, err := s.Pair(code2)
	if err != ErrAlreadyPaired {
		t.Errorf("err: got %v, want ErrAlreadyPaired", err)
	}
}

func TestGenerateCode_PurgesExpired(t *testing.T) {
	dir := tempDir(t)
	s, _ := NewStore(dir)

	s.mu.Lock()
	s.data.Pending["expired1"] = PendingEntry{UserID: "u-x", ExpiresAt: time.Now().Add(-1 * time.Hour)}
	s.data.Pending["expired2"] = PendingEntry{UserID: "u-y", ExpiresAt: time.Now().Add(-1 * time.Hour)}
	s.mu.Unlock()

	_, _ = s.GenerateCode("u-fresh")
	// expired codes should have been purged; only the fresh one remains.
	if s.PendingCount() != 1 {
		t.Errorf("pending: got %d, want 1", s.PendingCount())
	}
}
