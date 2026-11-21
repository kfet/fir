//go:build !unix

package cache

import (
	"errors"
	"os"
	"time"
)

// No flock here: Sweep falls back to the age-based sweep.
func sweep(base, keep string) { SweepAged(base, keep, MaxAge, NotDotted) }

// stampTime is now: the age sweep reads the mtime as "last used".
func stampTime() (time.Time, bool) { return time.Now(), true }

var errNoLock = errors.New("cache: no file locks on this platform")

func lockShared(*os.File) error       { return errNoLock }
func tryLockExclusive(*os.File) error { return errNoLock }
