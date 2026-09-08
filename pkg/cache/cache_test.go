package cache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDir_PrefersXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	got, err := Dir("sdks")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join("/xdg", "fir", "sdks"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDir_FallsBackToHomeDotCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	homeDir = func() (string, error) { return "/home/someone", nil }
	t.Cleanup(func() { homeDir = os.UserHomeDir })

	got, err := Dir("builtin-skills")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join("/home/someone", ".cache", "fir", "builtin-skills"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDir_ReportsHomeResolutionFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	homeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { homeDir = os.UserHomeDir })

	if _, err := Dir("sdks"); err == nil {
		t.Fatal("Dir with an unresolvable home = nil error")
	}
}

func TestClaim_RefreshesMtime(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	Claim(dir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Fatalf("mtime not refreshed: %v", info.ModTime())
	}
	// A directory that does not exist is not an error worth surfacing.
	Claim(filepath.Join(dir, "gone"))
}

func TestNotDotted(t *testing.T) {
	for name, want := range map[string]bool{"abc": true, ".extract-1": false, "": false} {
		if got := NotDotted(name); got != want {
			t.Errorf("NotDotted(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSweepAged(t *testing.T) {
	base := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
		return p
	}

	keep := mk("keep", 30*24*time.Hour)   // old, but claimed by this process
	fresh := mk("fresh", time.Hour)       // young
	stale := mk("stale", 30*24*time.Hour) // old and unclaimed
	dotted := mk(".extract-abc", 30*24*time.Hour)
	file := filepath.Join(base, "loose.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	SweepAged(base, keep, 14*24*time.Hour, NotDotted)

	for _, p := range []string{keep, fresh, dotted, file} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was collected but should have survived: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale survived (err=%v)", err)
	}
}

func TestSweepAged_UnreadableBaseIsNotAnError(t *testing.T) {
	SweepAged(filepath.Join(t.TempDir(), "nope"), "", time.Hour, NotDotted)
}

// TestSweepAged_SkipsEntriesThatVanish: ReadDir and the per-entry stat
// are two syscalls apart, and a sibling fir process sweeping the same
// cache can delete an entry in between. That must be a skip, not a
// deletion attempt on a path we know nothing about.
func TestSweepAged_SkipsEntriesThatVanish(t *testing.T) {
	base := t.TempDir()
	victim := filepath.Join(base, "vanishing")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}

	statDir = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	t.Cleanup(func() { statDir = os.Stat })

	SweepAged(base, "", time.Hour, NotDotted)

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("entry was removed despite an unreadable stat: %v", err)
	}
}
