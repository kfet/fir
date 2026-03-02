package extension

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeHash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := ComputeHash(p)
	if err != nil {
		t.Fatal(err)
	}
	// SHA-256 of "hello\n"
	const want = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if h != want {
		t.Fatalf("got %s, want %s", h, want)
	}
}

func TestComputeHash_Missing(t *testing.T) {
	_, err := ComputeHash("/no/such/file")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestIsTrusted_Unknown(t *testing.T) {
	ts := NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json"))
	if ts.IsTrusted("/proj", "ext", "abc123") {
		t.Fatal("expected false for unknown extension")
	}
}

func TestRecordAndIsTrusted(t *testing.T) {
	ts := NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json"))
	if err := ts.RecordTrust("/proj", "ext", "hash1"); err != nil {
		t.Fatal(err)
	}
	if !ts.IsTrusted("/proj", "ext", "hash1") {
		t.Fatal("expected trusted after RecordTrust")
	}
	// Different hash should not be trusted.
	if ts.IsTrusted("/proj", "ext", "hash2") {
		t.Fatal("expected false for different hash")
	}
}

func TestRevokeTrust(t *testing.T) {
	ts := NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json"))
	if err := ts.RecordTrust("/proj", "ext", "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.RevokeTrust("/proj", "ext"); err != nil {
		t.Fatal(err)
	}
	if ts.IsTrusted("/proj", "ext", "hash1") {
		t.Fatal("expected false after revoke")
	}
}

func TestFileCreatedOnFirstRecord(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "trust.json")
	ts := NewTrustStoreWithPath(p)
	if err := ts.RecordTrust("/proj", "ext", "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("trust file not created: %v", err)
	}
}
