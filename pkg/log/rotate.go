package log

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// RotateConfig holds resolved debug-log rotation settings. Zero values are
// replaced with sensible defaults by newRotatingWriter.
type RotateConfig struct {
	MaxSizeMB         int  // rotate when the file reaches this size (MB)
	Keep              int  // number of compressed backups to retain
	Compress          bool // gzip backups (else plain copies)
	CheckEveryWrites  int  // stat the file at most once per N writes
	CheckEverySeconds int  // ...or once per this many seconds, whichever first
}

func (c RotateConfig) withDefaults() RotateConfig {
	if c.MaxSizeMB <= 0 {
		c.MaxSizeMB = 100
	}
	if c.Keep <= 0 {
		c.Keep = 20
	}
	if c.CheckEveryWrites <= 0 {
		c.CheckEveryWrites = 1000
	}
	if c.CheckEverySeconds <= 0 {
		c.CheckEverySeconds = 30
	}
	return c
}

// rotatingWriter is an io.WriteCloser that wraps an O_APPEND log file and
// rotates it in place when it grows past a size threshold.
//
// Cross-process coordination uses an advisory flock on a sidecar lock file
// (<path>.lock): each write takes a shared lock, rotation takes an exclusive
// lock that drains in-flight writes and blocks new ones. In-process writes are
// serialised by mu so the shared/exclusive flock transitions never overlap
// within a single process.
type rotatingWriter struct {
	path     string
	cfg      RotateConfig
	maxBytes int64

	mu       sync.Mutex // serialises in-process writes / rotation critical section
	file     *os.File   // the O_APPEND log file
	lockFile *os.File   // sidecar lock file for write/rotate flock barrier
	archLock *os.File   // sidecar lock serialising the backup shuffle

	writeCount int64 // atomic
	lastCheck  int64 // atomic: unix nanos of last size check
	rotating   int32 // atomic: guards against concurrent rotation attempts

	wg     sync.WaitGroup // tracks detached gzip goroutines
	closed atomic.Bool
}

// newRotatingWriter opens (creating if necessary) the log file at path and its
// sidecar lock file, returning a writer that rotates in place.
func newRotatingWriter(path string, cfg RotateConfig) (*rotatingWriter, error) {
	cfg = cfg.withDefaults()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		f.Close()
		return nil, err
	}
	archLock, err := os.OpenFile(path+".archive.lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		f.Close()
		lockFile.Close()
		return nil, err
	}
	return &rotatingWriter{
		path:      path,
		cfg:       cfg,
		maxBytes:  int64(cfg.MaxSizeMB) * 1024 * 1024,
		file:      f,
		lockFile:  lockFile,
		archLock:  archLock,
		lastCheck: time.Now().UnixNano(),
	}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	if w.closed.Load() {
		return len(p), nil
	}
	w.mu.Lock()
	// Shared lock: blocks while another process is rotating (holds EX).
	_ = syscall.Flock(int(w.lockFile.Fd()), syscall.LOCK_SH)
	n, err := w.file.Write(p)
	_ = syscall.Flock(int(w.lockFile.Fd()), syscall.LOCK_UN)
	w.mu.Unlock()

	if err == nil && w.shouldCheck() {
		w.maybeRotate()
	}
	return n, err
}

// shouldCheck reports whether it is time to stat the file for size. It is
// lock-free on the hot path: an atomic counter plus a monotonic time gate.
func (w *rotatingWriter) shouldCheck() bool {
	c := atomic.AddInt64(&w.writeCount, 1)
	if c%int64(w.cfg.CheckEveryWrites) == 0 {
		return true
	}
	now := time.Now().UnixNano()
	last := atomic.LoadInt64(&w.lastCheck)
	if now-last >= int64(w.cfg.CheckEverySeconds)*int64(time.Second) {
		return atomic.CompareAndSwapInt64(&w.lastCheck, last, now)
	}
	return false
}

func (w *rotatingWriter) maybeRotate() {
	atomic.StoreInt64(&w.lastCheck, time.Now().UnixNano())
	fi, err := os.Stat(w.path)
	if err != nil || fi.Size() < w.maxBytes {
		return
	}
	// Only one rotation at a time within this process. The flag stays set
	// until the detached archive goroutine completes, so a second rotation
	// (and its backup shuffle) cannot overlap the first.
	if !atomic.CompareAndSwapInt32(&w.rotating, 0, 1) {
		return
	}
	w.rotate()
}

// rotate performs the loss-free in-place rotation. The critical section under
// the exclusive flock is kept tiny: a raw byte copy then a truncate. The
// expensive gzip + backup shuffle runs detached, outside the write barrier.
// rotate owns clearing the w.rotating flag.
func (w *rotatingWriter) rotate() {
	w.mu.Lock()
	_ = syscall.Flock(int(w.lockFile.Fd()), syscall.LOCK_EX)

	// Re-stat under the exclusive lock; another process may have rotated.
	fi, err := os.Stat(w.path)
	if err != nil || fi.Size() < w.maxBytes {
		_ = syscall.Flock(int(w.lockFile.Fd()), syscall.LOCK_UN)
		w.mu.Unlock()
		atomic.StoreInt32(&w.rotating, 0)
		return
	}
	size := fi.Size()

	tmp, copyErr := w.copyAndTruncate(size)

	_ = syscall.Flock(int(w.lockFile.Fd()), syscall.LOCK_UN)
	w.mu.Unlock()

	if copyErr != nil {
		if tmp != "" {
			os.Remove(tmp)
		}
		atomic.StoreInt32(&w.rotating, 0)
		return
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer atomic.StoreInt32(&w.rotating, 0)
		w.archive(tmp)
	}()
}

// copyAndTruncate raw-copies bytes [0,size) of the log into a temp file then
// truncates the log to zero. Same inode survives, so O_APPEND writers need no
// reopen. Returns the temp path. Must be called under the exclusive lock.
func (w *rotatingWriter) copyAndTruncate(size int64) (string, error) {
	dir := filepath.Dir(w.path)
	tmpf, err := os.CreateTemp(dir, "debug.log.rot-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpf.Name()

	src, err := os.Open(w.path)
	if err != nil {
		tmpf.Close()
		return tmpPath, err
	}
	_, err = io.CopyN(tmpf, src, size)
	src.Close()
	if cerr := tmpf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return tmpPath, err
	}
	if err := os.Truncate(w.path, 0); err != nil {
		return tmpPath, err
	}
	return tmpPath, nil
}

// archive shifts existing backups and writes the new one from tmp, then
// removes tmp. Runs detached, holding no write barrier — but takes a dedicated
// cross-process lock so concurrent rotations (this process or another) cannot
// clobber each other's backup shuffle.
func (w *rotatingWriter) archive(tmp string) {
	_ = syscall.Flock(int(w.archLock.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(w.archLock.Fd()), syscall.LOCK_UN)

	suffix := ".gz"
	if !w.cfg.Compress {
		suffix = ""
	}
	name := func(i int) string {
		return fmt.Sprintf("%s.%d%s", w.path, i, suffix)
	}

	// Drop the oldest, shift the rest up by one.
	os.Remove(name(w.cfg.Keep))
	for i := w.cfg.Keep - 1; i >= 1; i-- {
		_ = os.Rename(name(i), name(i+1))
	}

	if w.cfg.Compress {
		if err := gzipFile(tmp, name(1)); err != nil {
			// Keep tmp on failure so the rotated bytes are not lost.
			return
		}
	} else {
		if err := copyFile(tmp, name(1)); err != nil {
			return
		}
	}
	os.Remove(tmp)
}

func (w *rotatingWriter) Close() error {
	if w.closed.Swap(true) {
		return nil
	}
	w.wg.Wait()
	err := w.file.Close()
	if lerr := w.lockFile.Close(); err == nil {
		err = lerr
	}
	if aerr := w.archLock.Close(); err == nil {
		err = aerr
	}
	return err
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	gw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	_, cerr := io.Copy(gw, in)
	if zerr := gw.Close(); cerr == nil {
		cerr = zerr
	}
	if oerr := out.Close(); cerr == nil {
		cerr = oerr
	}
	if cerr != nil {
		os.Remove(dst)
	}
	return cerr
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if oerr := out.Close(); cerr == nil {
		cerr = oerr
	}
	return cerr
}
