package permq

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnqueueDrain(t *testing.T) {
	q := New("")
	q.Enqueue("u-1", Request{RequestID: "r1", ToolName: "bash", Description: "run ls"})
	q.Enqueue("u-1", Request{RequestID: "r2", ToolName: "write", Description: "write foo"})

	if q.Len("u-1") != 2 {
		t.Fatalf("len: got %d want 2", q.Len("u-1"))
	}
	reqs := q.Drain("u-1")
	if len(reqs) != 2 {
		t.Fatalf("drain: got %d want 2", len(reqs))
	}
	if reqs[0].RequestID != "r1" || reqs[1].RequestID != "r2" {
		t.Errorf("order wrong: %+v", reqs)
	}
	if q.Len("u-1") != 0 {
		t.Errorf("after drain: %d", q.Len("u-1"))
	}
}

func TestDrainEmpty(t *testing.T) {
	q := New("")
	if reqs := q.Drain("u-none"); reqs != nil {
		t.Errorf("got %v want nil", reqs)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	q := New(dir)
	q.Enqueue("u-persist", Request{RequestID: "r1", ToolName: "t", QueuedAt: time.Now()})

	// Reload from disk.
	q2 := New(dir)
	if q2.Len("u-persist") != 1 {
		t.Fatalf("after reload: %d", q2.Len("u-persist"))
	}
	reqs := q2.Drain("u-persist")
	if reqs[0].RequestID != "r1" {
		t.Errorf("wrong id: %s", reqs[0].RequestID)
	}
	// File should be removed after drain.
	p := filepath.Join(dir, "permq", "u-persist.json")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("file not removed after drain")
	}
}

func TestFormatDrain(t *testing.T) {
	q := New("")
	q.Enqueue("u-fmt", Request{RequestID: "r1", ToolName: "bash", Description: "run cmd"})
	s := q.FormatDrain("u-fmt")
	if s == "" {
		t.Fatal("empty format")
	}
	for _, want := range []string{"pending permission", "bash", "run cmd", "r1", "/allow", "/deny"} {
		if !contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// Second call should be empty.
	if s2 := q.FormatDrain("u-fmt"); s2 != "" {
		t.Errorf("second drain not empty: %s", s2)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
