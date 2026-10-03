package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/session/store"
)

func TestParseArgs_ResumeAt(t *testing.T) {
	a := ParseArgs([]string{"--resume", "abcd", "--at", "e5", "hello"})
	if a.ResumeRef != "abcd" || a.At != "e5" || !a.Resume {
		t.Fatalf("got ResumeRef=%q At=%q Resume=%v", a.ResumeRef, a.At, a.Resume)
	}
	// Without --at, -r keeps its old meaning and does not eat the prompt.
	if a := ParseArgs([]string{"-r", "hello"}); a.ResumeRef != "" {
		t.Fatalf("-r without --at consumed %q", a.ResumeRef)
	}
}

func TestForkSessionStoreAt(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	dir := store.DefaultSessionDir(agentDir, cwd)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "2026_parent-uuid.jsonl")
	lines := []string{
		`{"type":"session","version":3,"id":"parent-uuid","timestamp":"2026-08-02T07:00:00Z","cwd":"/tmp"}`,
		`{"type":"message","id":"e1","parentId":"","timestamp":"2026-08-02T07:00:01Z","message":{"role":"user","content":"hi","timestamp":1}}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-08-02T07:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"yo"}],"stopReason":"stop","timestamp":2}}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-08-02T07:00:03Z","message":{"role":"user","content":"more","timestamp":3}}`,
	}
	if err := os.WriteFile(src, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ss, err := forkSessionStoreAt(&Args{ResumeRef: "parent-uuid", At: "e2"}, cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	if ss.GetCwd() != cwd {
		t.Errorf("child cwd = %q, want %q", ss.GetCwd(), cwd)
	}
	if ss.GetLeafID() != "e2" || ss.GetSessionFile() == src {
		t.Fatalf("leaf=%q file=%q", ss.GetLeafID(), ss.GetSessionFile())
	}

	if a := ParseArgs([]string{"--resume", "x", "--at"}); !a.Seen["--at"] || a.At != "" {
		t.Fatalf("bare --at: Seen=%v At=%q", a.Seen["--at"], a.At)
	}
	if _, err := forkSessionStoreAt(&Args{ResumeRef: "parent-uuid"}, cwd, agentDir); err == nil {
		t.Error("expected error for --at without an entry id")
	}
	if _, err := forkSessionStoreAt(&Args{At: "e2"}, cwd, agentDir); err == nil {
		t.Error("expected error without a session reference")
	}
	if _, err := forkSessionStoreAt(&Args{ResumeRef: "nope", At: "e2"}, cwd, agentDir); err == nil {
		t.Error("expected error for unknown session")
	}
}
