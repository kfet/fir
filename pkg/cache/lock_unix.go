//go:build unix

package cache

import (
	"os"
	"syscall"
	"time"
)

func sweep(base, keep string) { sweepLocked(base, keep) }

// stampTime is the build time: Sweep orders trees by it.
func stampTime() (time.Time, bool) { return buildTime() }

func lockShared(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_SH) }

func tryLockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
