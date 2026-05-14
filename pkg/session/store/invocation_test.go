package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionInvocation_IsEmpty(t *testing.T) {
	if !(*SessionInvocation)(nil).IsEmpty() {
		t.Error("nil invocation should be empty")
	}
	if !(&SessionInvocation{}).IsEmpty() {
		t.Error("zero-value invocation should be empty")
	}
	if (&SessionInvocation{Model: "claude"}).IsEmpty() {
		t.Error("invocation with Model should not be empty")
	}
	if (&SessionInvocation{NoMCP: true}).IsEmpty() {
		t.Error("invocation with NoMCP should not be empty")
	}
	if (&SessionInvocation{Extensions: []string{"foo"}}).IsEmpty() {
		t.Error("invocation with Extensions should not be empty")
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1 := HashFile(p)
	if h1 == "" {
		t.Fatal("expected non-empty hash")
	}
	if HashFile(p) != h1 {
		t.Error("hash not stable")
	}
	if err := os.WriteFile(p, []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if HashFile(p) == h1 {
		t.Error("hash should change after content change")
	}
	if HashFile(filepath.Join(dir, "missing")) != "" {
		t.Error("missing file should return empty hash")
	}
	if HashFile("") != "" {
		t.Error("empty path should return empty hash")
	}
}

func TestStampAndLoadInvocation(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionStore(dir, dir)

	inv := &SessionInvocation{
		Model:        "claude-sonnet",
		MCPConfig:    "/tmp/x.json",
		Extensions:   []string{"demo", "schedule"},
		NoExtensions: false,
	}
	sm.StampInvocation(inv)

	got := sm.GetInvocation()
	if got == nil {
		t.Fatal("GetInvocation returned nil after stamp")
	}
	if got.Model != "claude-sonnet" || got.MCPConfig != "/tmp/x.json" {
		t.Errorf("invocation fields not preserved: %+v", got)
	}
	if len(got.Extensions) != 2 || got.Extensions[0] != "demo" {
		t.Errorf("extensions not preserved: %+v", got.Extensions)
	}

	// Read back from disk via LoadInvocation.
	loaded := LoadInvocation(sm.GetSessionFile())
	if loaded == nil {
		t.Fatal("LoadInvocation returned nil")
	}
	if loaded.Model != "claude-sonnet" {
		t.Errorf("loaded model: got %q want %q", loaded.Model, "claude-sonnet")
	}

	// Second stamp must be a no-op (never overwrite).
	sm.StampInvocation(&SessionInvocation{Model: "other"})
	if sm.GetInvocation().Model != "claude-sonnet" {
		t.Error("StampInvocation overwrote existing stamp")
	}

	// Empty stamp must be a no-op (no header rewrite, original preserved).
	sm.StampInvocation(&SessionInvocation{})
	if sm.GetInvocation().Model != "claude-sonnet" {
		t.Error("empty stamp clobbered existing")
	}
}

func TestLoadInvocation_LegacySessionHasNone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy.jsonl")
	header := SessionHeader{
		Type:      "session",
		Version:   CurrentSessionVersion,
		ID:        "legacy-id",
		Timestamp: "2024-01-01T00:00:00Z",
		Cwd:       dir,
	}
	b, _ := json.Marshal(header)
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if inv := LoadInvocation(p); inv != nil {
		t.Errorf("legacy session should have nil invocation, got %+v", inv)
	}
}

func TestLoadInvocation_MalformedReturnsNil(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(p, []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if inv := LoadInvocation(p); inv != nil {
		t.Errorf("malformed file should give nil, got %+v", inv)
	}
	if inv := LoadInvocation(filepath.Join(dir, "missing")); inv != nil {
		t.Error("missing file should give nil")
	}
}

func TestStampInvocation_AfterEntriesIsNoop(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionStore(dir, dir)

	// Append a non-message entry to advance past the empty-entries gate.
	sm.AppendCustomEntry("note", json.RawMessage(`{"x":1}`))

	sm.StampInvocation(&SessionInvocation{Model: "claude"})
	if sm.GetInvocation() != nil {
		t.Error("StampInvocation should be no-op after entries exist")
	}
}

func TestStampInvocation_OnDiskHeaderHasInvocation(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionStore(dir, dir)
	sm.StampInvocation(&SessionInvocation{
		Model:     "claude",
		MCPConfig: "/x.json",
	})

	data, err := os.ReadFile(sm.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	firstLine := string(data)
	if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
		firstLine = firstLine[:i]
	}
	if !strings.Contains(firstLine, `"invocation"`) {
		t.Errorf("on-disk header missing invocation field: %s", firstLine)
	}
	if !strings.Contains(firstLine, `"claude"`) {
		t.Errorf("on-disk header missing model: %s", firstLine)
	}
}
