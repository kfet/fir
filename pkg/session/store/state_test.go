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
