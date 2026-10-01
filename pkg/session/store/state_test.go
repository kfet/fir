package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandles_BindResolveForgetPrune(t *testing.T) {
	agentDir := t.TempDir()
	file := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ResolveHandle(agentDir, "h") != "" {
		t.Fatal("unbound handle resolved")
	}
	if err := BindHandle(agentDir, "h", file); err != nil {
		t.Fatal(err)
	}
	if got := ResolveHandle(agentDir, "h"); got != file {
		t.Fatalf("resolve = %q", got)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(handlePath(agentDir, "h"), old, old)
	PruneHandles(agentDir, time.Now().Add(-2*time.Hour))
	if ResolveHandle(agentDir, "h") == "" {
		t.Fatal("fresh handle pruned")
	}
	PruneHandles(agentDir, time.Now())
	if ResolveHandle(agentDir, "h") != "" {
		t.Fatal("stale handle kept")
	}
	_ = BindHandle(agentDir, "h", file)
	if !ForgetHandle(agentDir, "h") || ResolveHandle(agentDir, "h") != "" {
		t.Fatal("forget failed")
	}
	// A binding to a deleted transcript does not resolve.
	_ = BindHandle(agentDir, "h", file)
	_ = os.Remove(file)
	if ResolveHandle(agentDir, "h") != "" {
		t.Fatal("handle to missing transcript resolved")
	}
}

func TestWriteState_0600AndForkCopies(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionStore(dir, dir)
	file := ss.GetSessionFile()
	if err := WriteState(file, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(StatePath(file))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	fork, err := ForkFrom(file, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	var got map[string]string
	if !ReadState(fork.GetSessionFile(), &got) || got["k"] != "v" {
		t.Errorf("fork state = %v", got)
	}
	ss.Close()
}

func TestStateHistory_AppendsOnChangeOnly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.jsonl")
	for _, v := range []string{"a", "a", "b", "b", "c"} {
		if err := WriteState(file, map[string]string{"k": v}); err != nil {
			t.Fatal(err)
		}
	}
	recs := ReadStateHistory(file)
	if len(recs) != 3 {
		t.Fatalf("records = %d, want 3", len(recs))
	}
	var prev time.Time
	for i, r := range recs {
		ts, err := time.Parse(time.RFC3339Nano, r.TS)
		if err != nil {
			t.Fatalf("rec %d ts %q: %v", i, r.TS, err)
		}
		if ts.Before(prev) {
			t.Errorf("rec %d out of order", i)
		}
		prev = ts
	}
	var got map[string]string
	if !ReadState(file, &got) || got["k"] != "c" {
		t.Errorf("latest = %v", got)
	}
	fi, _ := os.Stat(StatePath(file))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestStateHistory_TornLastLineIgnored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.jsonl")
	_ = WriteState(file, map[string]string{"k": "good"})
	f, _ := os.OpenFile(StatePath(file), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"ts":"2026-01-01T00:00:00Z","state":{"k":"to`)
	f.Close()
	var got map[string]string
	if !ReadState(file, &got) || got["k"] != "good" {
		t.Fatalf("restore = %v", got)
	}
	// Next append starts on a fresh line and is readable.
	_ = WriteState(file, map[string]string{"k": "next"})
	if !ReadState(file, &got) || got["k"] != "next" {
		t.Fatalf("after torn append = %v", got)
	}
}

func TestStateHistory_MigratesLegacy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(legacyStatePath(file), []byte(`{"k":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if !ReadState(file, &got) || got["k"] != "old" {
		t.Fatalf("migrated = %v", got)
	}
	if _, err := os.Stat(legacyStatePath(file)); !os.IsNotExist(err) {
		t.Error("legacy file not removed")
	}
	if n := len(ReadStateHistory(file)); n != 1 {
		t.Errorf("records = %d", n)
	}
	// Saving the same state after migration adds nothing.
	_ = WriteState(file, map[string]string{"k": "old"})
	if n := len(ReadStateHistory(file)); n != 1 {
		t.Errorf("records after no-op = %d", n)
	}
}

func TestStateHistory_Compacts(t *testing.T) {
	old := MaxStateHistory
	MaxStateHistory = 10
	defer func() { MaxStateHistory = old }()
	file := filepath.Join(t.TempDir(), "s.jsonl")
	for i := 0; i < MaxStateHistory+5; i++ {
		_ = WriteState(file, map[string]int{"i": i})
	}
	recs := ReadStateHistory(file)
	if len(recs) != MaxStateHistory {
		t.Fatalf("records = %d", len(recs))
	}
	var got map[string]int
	if !ReadState(file, &got) || got["i"] != MaxStateHistory+4 {
		t.Errorf("latest = %v", got)
	}
}
