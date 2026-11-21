//go:build unix

package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mkTree(t *testing.T, base, name string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(dir, mt, mt); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// holdOther holds a shared lock on dir.lock through a separate open
// file, as another running fir would: flock is per open file, so it
// conflicts with the sweep's exclusive try-lock.
func holdOther(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := lockShared(f); err != nil {
		t.Fatal(err)
	}
}

// TestSweep: an older tree nobody holds is deleted; a held one, a newer
// one, the own tree, dotfiles and plain files stay; .lock files are never
// deleted.
func TestSweep(t *testing.T) {
	base := t.TempDir()
	own := mkTree(t, base, "own", time.Hour)
	older := mkTree(t, base, "older", 2*time.Hour)
	held := mkTree(t, base, "held", 2*time.Hour)
	newer := mkTree(t, base, "newer", 0)
	equal := mkTree(t, base, "equal", time.Hour)
	if err := os.Chtimes(equal, mustStat(t, own).ModTime(), mustStat(t, own).ModTime()); err != nil {
		t.Fatal(err)
	}
	dotted := mkTree(t, base, ".extract-x", 2*time.Hour)
	if err := os.WriteFile(filepath.Join(base, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	holdOther(t, held)
	for _, p := range []string{older, equal, newer} {
		if err := os.WriteFile(p+".lock", nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unlockedYoung := mkTree(t, base, "prelock-young", 2*time.Hour)
	unlockedOld := mkTree(t, base, "prelock-old", MaxAge+time.Hour)

	Sweep(base, own)
	if !exists(unlockedYoung) {
		t.Error("pre-lock tree deleted before MaxAge")
	}
	if exists(unlockedOld) {
		t.Error("pre-lock tree older than MaxAge survived")
	}

	for _, p := range []string{own, held, newer, equal, dotted} {
		if !exists(p) {
			t.Errorf("%s deleted", filepath.Base(p))
		}
	}
	if exists(older) {
		t.Error("older unheld tree survived")
	}
	if !exists(older + ".lock") {
		t.Error("lock file deleted")
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// TestSweep_HeldByThisProcess: Hold protects a tree from a sweep.
func TestSweep_HeldByThisProcess(t *testing.T) {
	base := t.TempDir()
	own := mkTree(t, base, "own", 0)
	mine := mkTree(t, base, "mine", time.Hour)
	Hold(mine)
	if !exists(mine + ".lock") {
		t.Fatal("Hold did not create the lock file")
	}
	t.Cleanup(func() {
		for _, f := range held {
			f.Close()
		}
		held = map[string]*os.File{}
	})
	Hold(mine) // a second Hold of the same tree opens nothing
	if len(held) != 1 {
		t.Fatalf("held = %d", len(held))
	}
	// flock is per open file: a second open in this process conflicts.
	Sweep(base, own)
	if !exists(mine) {
		t.Fatal("held tree deleted")
	}
}

func TestSweep_MissingOwnOrBase(t *testing.T) {
	base := t.TempDir()
	other := mkTree(t, base, "other", time.Hour)
	Sweep(base, filepath.Join(base, "gone"))
	if !exists(other) {
		t.Fatal("swept without an own tree")
	}
	Sweep(filepath.Join(base, "nobase"), other) // no panic
}

func TestSweep_LockFileUnopenable(t *testing.T) {
	base := t.TempDir()
	own := mkTree(t, base, "own", 0)
	older := mkTree(t, base, "older", time.Hour)
	if err := os.WriteFile(older+".lock", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(older+".lock", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(older+".lock", 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	Sweep(base, own)
	if !exists(older) {
		t.Fatal("deleted without the lock")
	}
}

func TestHold_Failures(t *testing.T) {
	t.Cleanup(func() { held = map[string]*os.File{} })
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	Hold(filepath.Join(ro, "x"))        // cannot create the lock file
	Hold(filepath.Join(ro, "sub", "x")) // cannot create the base
	if len(held) != 0 {
		t.Fatalf("held = %d", len(held))
	}
}

func TestHold_LockFails(t *testing.T) {
	t.Cleanup(func() { held = map[string]*os.File{}; lockSharedFn = lockShared })
	lockSharedFn = func(*os.File) error { return os.ErrPermission }
	Hold(filepath.Join(t.TempDir(), "x"))
	if len(held) != 0 {
		t.Fatalf("held = %d", len(held))
	}
}

func TestStamp(t *testing.T) {
	t.Cleanup(func() { buildTime = readBuildTime })
	dir := t.TempDir()
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	buildTime = func() (time.Time, bool) { return want, true }
	Stamp(dir)
	if got := mustStat(t, dir).ModTime(); !got.Equal(want) {
		t.Fatalf("mtime = %v", got)
	}
	buildTime = func() (time.Time, bool) { return time.Time{}, false }
	Stamp(dir) // no-op
	if got := mustStat(t, dir).ModTime(); !got.Equal(want) {
		t.Fatalf("mtime changed to %v", got)
	}
}

func TestReadBuildTime(t *testing.T) {
	t.Cleanup(func() { readBuildInfo = defaultReadBuildInfo })
	readBuildInfo = func() (*debugBuildInfo, bool) { return nil, false }
	if _, ok := readBuildTime(); ok {
		t.Fatal("no build info")
	}
	readBuildInfo = func() (*debugBuildInfo, bool) {
		return &debugBuildInfo{Settings: []debugBuildSetting{{Key: "vcs.time", Value: "2026-11-20T10:00:00Z"}}}, true
	}
	if got, ok := readBuildTime(); !ok || got.Year() != 2026 {
		t.Fatalf("got %v %v", got, ok)
	}
	readBuildInfo = func() (*debugBuildInfo, bool) {
		return &debugBuildInfo{Settings: []debugBuildSetting{{Key: "vcs.time", Value: "bad"}}}, true
	}
	if _, ok := readBuildTime(); ok {
		t.Fatal("bad time accepted")
	}
	readBuildInfo = func() (*debugBuildInfo, bool) { return &debugBuildInfo{}, true }
	if _, ok := readBuildTime(); ok {
		t.Fatal("no vcs.time")
	}
}

// TestSweep_PreLockTreeNewerThanOwn: a pre-lock tree that an old fir
// touched after this binary was built still ages out.
func TestSweep_PreLockTreeNewerThanOwn(t *testing.T) {
	base := t.TempDir()
	own := mkTree(t, base, "own", 40*24*time.Hour)
	prelock := mkTree(t, base, "prelock", MaxAge+time.Hour)
	Sweep(base, own)
	if exists(prelock) {
		t.Fatal("pre-lock tree newer than own survived MaxAge")
	}
}

// TestSweep_EntryVanishes: a tree removed between ReadDir and stat is skipped.
func TestSweep_EntryVanishes(t *testing.T) {
	base := t.TempDir()
	own := mkTree(t, base, "own", 0)
	other := mkTree(t, base, "other", time.Hour)
	t.Cleanup(func() { statDir = os.Stat })
	statDir = func(p string) (os.FileInfo, error) {
		if p == other {
			return nil, os.ErrNotExist
		}
		return os.Stat(p)
	}
	Sweep(base, own)
	if !exists(other) {
		t.Fatal("deleted a tree it could not stat")
	}
}
