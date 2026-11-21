package cache

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// Ownership of an extracted tree is a kernel lock, not an age. Every
// process that uses <base>/<key>/ holds a shared lock on <base>/<key>.lock
// for its whole life. Sweep deletes another tree only when it can take
// that lock exclusively, so a tree is never removed under a running
// process, and a crashed process leaves nothing behind (the kernel drops
// its lock). The .lock files are never deleted: unlinking one while
// another process opens it would split the lock across two inodes.

// lockSharedFn is indirected for tests.
var lockSharedFn = lockShared

// held keeps the lock files open for the life of the process, one per
// tree: a second Hold of the same tree is a no-op.
var (
	heldMu sync.Mutex
	held   = map[string]*os.File{}
)

// buildTime is the commit time of this binary, used as the mtime of its
// trees so that Sweep's "older than mine" order follows the build, not
// the time a tree was last extracted.
var buildTime = readBuildTime

type (
	debugBuildInfo    = debug.BuildInfo
	debugBuildSetting = debug.BuildSetting
)

var defaultReadBuildInfo = debug.ReadBuildInfo

var readBuildInfo = defaultReadBuildInfo

func readBuildTime() (time.Time, bool) {
	info, ok := readBuildInfo()
	if !ok {
		return time.Time{}, false
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.time" {
			t, err := time.Parse(time.RFC3339, s.Value)
			return t, err == nil
		}
	}
	return time.Time{}, false
}

// Hold takes this process's shared lock on dir. Call it before checking
// or extracting dir. Best-effort: without a lock the tree still works,
// it is only unprotected from a sweep.
func Hold(dir string) {
	heldMu.Lock()
	defer heldMu.Unlock()
	if held[dir] != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return
	}
	if lockSharedFn(f) != nil {
		f.Close()
		return
	}
	held[dir] = f
}

// Stamp sets dir's mtime to this binary's build time. Best-effort, and a
// no-op when the binary carries no build time (go run, tests).
func Stamp(dir string) {
	if t, ok := stampTime(); ok {
		_ = os.Chtimes(dir, t, t)
	}
}

// Sweep deletes every tree under base that is strictly older than keep
// and that no process holds. It never touches a newer tree, so an old
// binary does not delete the trees of a newer one.
func Sweep(base, keep string) { sweep(base, keep) }

func sweepLocked(base, keep string) {
	own, err := statDir(keep)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(base, e.Name())
		if !e.IsDir() || !NotDotted(e.Name()) || path == keep {
			continue
		}
		info, err := statDir(path)
		if err != nil {
			continue
		}
		// No lock file: a fir from before locks may still use the tree
		// without holding anything, so only the old age rule applies.
		f, err := os.Open(path + ".lock")
		if err != nil {
			if os.IsNotExist(err) && time.Since(info.ModTime()) > MaxAge {
				os.RemoveAll(path)
			}
			continue
		}
		// Never a newer tree (or one built at the same time): an old
		// binary must not delete the trees of a newer one.
		if !info.ModTime().Before(own.ModTime()) {
			f.Close()
			continue
		}
		if tryLockExclusive(f) == nil {
			os.RemoveAll(path)
		}
		f.Close()
	}
}
