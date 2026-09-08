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

// TestDir_DefersToThePlatformCacheDir: ~/.cache is the XDG answer and is
// canonical on Linux only — on macOS the OS's cache directory is
// ~/Library/Caches. Dir must ask os.UserCacheDir rather than assume.
func TestDir_DefersToThePlatformCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	userCacheDir = func() (string, error) { return "/Users/someone/Library/Caches", nil }
	t.Cleanup(func() { userCacheDir = os.UserCacheDir })

	got, err := Dir("builtin-skills")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join("/Users/someone/Library/Caches", "fir", "builtin-skills"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

// A relative $XDG_CACHE_HOME is invalid per the spec and must not be
// joined onto anything; the platform answer wins.
func TestDir_IgnoresRelativeXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "relative/cache")
	userCacheDir = func() (string, error) { return "/platform", nil }
	t.Cleanup(func() { userCacheDir = os.UserCacheDir })

	got, err := Dir("sdks")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join("/platform", "fir", "sdks"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestDir_ReportsPlatformResolutionFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	userCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	t.Cleanup(func() { userCacheDir = os.UserCacheDir })

	if _, err := Dir("sdks"); err == nil {
		t.Fatal("Dir with an unresolvable cache dir = nil error")
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

// TestSweepLegacy_CollectsTheOldDotCacheLocation: on a Mac (and on any
// host that points $XDG_CACHE_HOME elsewhere) fir's old hardcoded
// ~/.cache/fir/<sub> is no longer the cache dir, so it is now an
// abandoned location that nothing else will ever clean.
func TestSweepLegacy_CollectsTheOldDotCacheLocation(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = os.UserHomeDir })
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // current dir differs from ~/.cache

	legacy := filepath.Join(home, ".cache", "fir", "sdks")
	old := filepath.Join(legacy, "aaaa")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, ts, ts); err != nil {
		t.Fatal(err)
	}

	tmpBase := filepath.Join(t.TempDir(), "fir-builtin-extensions")
	tmpOld := filepath.Join(tmpBase, "bbbb")
	if err := os.MkdirAll(tmpOld, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmpOld, ts, ts); err != nil {
		t.Fatal(err)
	}

	SweepLegacy("sdks", tmpBase)

	for _, p := range []string{old, legacy, tmpOld, tmpBase} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived (err=%v)", p, err)
		}
	}
}

// Where ~/.cache IS the platform cache dir — plain Linux — the legacy
// location and the live one are the same path, and sweeping it would
// delete trees in current use.
func TestSweepLegacy_NoOpsWhenDotCacheIsTheRealCacheDir(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	userCacheDir = func() (string, error) { return filepath.Join(home, ".cache"), nil }
	t.Cleanup(func() {
		homeDir = os.UserHomeDir
		userCacheDir = os.UserCacheDir
	})
	t.Setenv("XDG_CACHE_HOME", "")

	live := filepath.Join(home, ".cache", "fir", "sdks", "aaaa")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(live, ts, ts); err != nil {
		t.Fatal(err)
	}

	SweepLegacy("sdks")

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live cache tree deleted as if it were legacy: %v", err)
	}
}

// An unresolvable home means there is no legacy path to reason about,
// not a reason to fail.
func TestSweepLegacy_SurvivesAnUnresolvableHome(t *testing.T) {
	homeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { homeDir = os.UserHomeDir })
	SweepLegacy("sdks")
}

// Nor is an unresolvable cache dir: with no current path to compare
// against, ~/.cache cannot be shown to be abandoned.
func TestSweepLegacy_SurvivesAnUnresolvableCacheDir(t *testing.T) {
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	userCacheDir = func() (string, error) { return "", errors.New("nope") }
	t.Cleanup(func() {
		homeDir = os.UserHomeDir
		userCacheDir = os.UserCacheDir
	})
	t.Setenv("XDG_CACHE_HOME", "")

	live := filepath.Join(home, ".cache", "fir", "sdks", "aaaa")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(live, ts, ts); err != nil {
		t.Fatal(err)
	}

	SweepLegacy("sdks")

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("swept a legacy path that could not be proven legacy: %v", err)
	}
}
