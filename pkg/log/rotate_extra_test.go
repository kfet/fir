package log

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestWriter opens a rotating writer under a fresh temp dir and closes it
// when the test ends.
func newTestWriter(t *testing.T, cfg RotateConfig) (*rotatingWriter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "debug.log")
	w, err := newRotatingWriter(path, cfg)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

// TestNewRotatingWriterSidecarFailures pins that a lock file fir cannot open
// is a hard error at Init time — the flock barrier is what stops two fir
// processes shredding each other's log during rotation, so starting without
// it must not be silently tolerated. Both sidecars are checked because they
// guard different critical sections.
func TestNewRotatingWriterSidecarFailures(t *testing.T) {
	for _, sidecar := range []string{".lock", ".archive.lock"} {
		t.Run(sidecar, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "debug.log")
			// A directory cannot be opened as a file.
			if err := os.MkdirAll(path+sidecar, 0o755); err != nil {
				t.Fatal(err)
			}

			w, err := newRotatingWriter(path, RotateConfig{})
			if err == nil {
				_ = w.Close()
				t.Fatal("expected an error when the sidecar lock cannot be opened")
			}
			if !strings.Contains(err.Error(), path+sidecar) {
				t.Errorf("error should name the sidecar, got: %v", err)
			}
		})
	}
}

// TestRotateOnTimeGate pins the second rotation trigger: a low-traffic
// process that never reaches CheckEveryWrites must still rotate once the
// check interval has elapsed. Without it a long-lived idle session can grow
// an unbounded log.
func TestRotateOnTimeGate(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{
		MaxSizeMB:        1,
		Keep:             2,
		Compress:         false,
		CheckEveryWrites: 1_000_000, // effectively never
	})

	// Put more than the cap in the file without tripping the write counter.
	big := make([]byte, 1024*1024+1024)
	for i := range big {
		big[i] = 'x'
	}
	big[len(big)-1] = '\n'
	if _, err := w.file.Write(big); err != nil {
		t.Fatal(err)
	}

	// Nothing yet: neither gate has fired.
	if _, err := w.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotation happened before either gate fired, err=%v", err)
	}

	// Age the last check past the interval; the next write must rotate.
	backdate := time.Now().Add(-time.Duration(w.cfg.CheckEverySeconds+1) * time.Second)
	w.lastCheck = backdate.UnixNano()

	if _, err := w.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	w.wg.Wait()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("time gate did not trigger a rotation: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= w.maxBytes {
		t.Errorf("live file not truncated: %d bytes", fi.Size())
	}
}

// TestRotateAbortsWhenAnotherProcessAlreadyRotated pins the re-stat under the
// exclusive lock: by the time we hold the barrier the file may already be
// small because another fir process rotated it. Rotating anyway would produce
// an empty backup and shift a real one off the end of the keep window.
func TestRotateAbortsWhenAnotherProcessAlreadyRotated(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1, Keep: 2, Compress: false})

	if _, err := w.Write([]byte("small\n")); err != nil {
		t.Fatal(err)
	}
	// Enter rotate() exactly as maybeRotate would, but with a file that is
	// under the cap — the state another process leaves behind.
	w.rotating = 1
	w.rotate()
	w.wg.Wait()

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("an aborted rotation must not create a backup, err=%v", err)
	}
	if w.rotating != 0 {
		t.Error("an aborted rotation must clear the rotating flag, or no further rotation can ever run")
	}
	// The live file is untouched.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "small\n" {
		t.Errorf("live file = %q, want it untouched", data)
	}
}

// TestRotateHandlesCopyFailure pins that a rotation which cannot copy the log
// out gives up cleanly: no truncation (the bytes are still in the live file),
// no temp file left in the log directory, and the rotating flag cleared so a
// later attempt can succeed.
func TestRotateHandlesCopyFailure(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1, Keep: 2, Compress: false})

	big := make([]byte, 1024*1024+16)
	for i := range big {
		big[i] = 'y'
	}
	if _, err := w.file.Write(big); err != nil {
		t.Fatal(err)
	}
	// The copy opens the log for reading; make that impossible.
	if err := os.Chmod(path, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	w.rotating = 1
	w.rotate()
	w.wg.Wait()

	if w.rotating != 0 {
		t.Error("a failed rotation must clear the rotating flag")
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Size() != int64(len(big)) {
		t.Errorf("live file = %d bytes, want the %d unrotated bytes kept", fi.Size(), len(big))
	}
	assertNoRotTempFiles(t, filepath.Dir(path))
}

// TestRotateHandlesTempCreateFailure covers the other copy failure: the temp
// file cannot be created at all, so there is nothing to clean up.
func TestRotateHandlesTempCreateFailure(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1, Keep: 2, Compress: false})

	big := make([]byte, 1024*1024+16)
	if _, err := w.file.Write(big); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w.rotating = 1
	w.rotate()
	w.wg.Wait()

	if w.rotating != 0 {
		t.Error("a failed rotation must clear the rotating flag")
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Size() != int64(len(big)) {
		t.Errorf("live file = %d bytes, want the unrotated bytes kept", fi.Size())
	}
}

// TestCopyAndTruncateShortFile pins the "log shrank under us" case: copying
// more bytes than the file holds must fail rather than truncate a file whose
// contents were never captured.
func TestCopyAndTruncateShortFile(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1})

	if _, err := w.file.Write([]byte("four\n")); err != nil {
		t.Fatal(err)
	}

	tmp, err := w.copyAndTruncate(1 << 20) // far more than is there
	if err == nil {
		t.Fatal("expected copying past the end of the log to fail")
	}
	if tmp == "" {
		t.Error("the temp path must be returned so the caller can clean it up")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "four\n" {
		t.Errorf("live file = %q, want it left untruncated after a failed copy", data)
	}
	_ = os.Remove(tmp)
}

// TestCopyAndTruncateReadOnlyLog pins that a log fir can read but not
// truncate is reported, and that the temp copy path comes back for cleanup.
func TestCopyAndTruncateReadOnlyLog(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1})

	if _, err := w.file.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	tmp, err := w.copyAndTruncate(6)
	if err == nil {
		t.Fatal("expected the truncate to fail on a read-only log")
	}
	if tmp == "" {
		t.Fatal("the temp path must be returned so the caller can clean it up")
	}
	// The bytes were captured before the failure, so nothing is lost.
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Errorf("temp copy = %q, want the log contents", data)
	}
	_ = os.Remove(tmp)
}

// TestArchiveUncompressed pins the Compress=false naming and copy path:
// backups are ".1", ".2", … with no .gz suffix, and hold the rotated bytes
// verbatim.
func TestArchiveUncompressed(t *testing.T) {
	w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1, Keep: 2, Compress: false})

	tmp := filepath.Join(filepath.Dir(path), "rotated-bytes")
	if err := os.WriteFile(tmp, []byte("rotated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.archive(tmp)

	data, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("expected an uncompressed backup: %v", err)
	}
	if string(data) != "rotated\n" {
		t.Errorf("backup = %q, want the rotated bytes", data)
	}
	if _, err := os.Stat(path + ".1.gz"); !os.IsNotExist(err) {
		t.Errorf("uncompressed config must not produce a .gz, err=%v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("a successful archive must remove its temp file, err=%v", err)
	}
}

// TestArchiveKeepsTempOnFailure pins the loss-avoidance rule stated in the
// code: if the backup cannot be written, the temp file holding the only copy
// of the rotated bytes must survive. Both compression modes are covered
// because they take different write paths.
func TestArchiveKeepsTempOnFailure(t *testing.T) {
	for _, compress := range []bool{true, false} {
		name := "plain"
		if compress {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			w, path := newTestWriter(t, RotateConfig{MaxSizeMB: 1, Keep: 2, Compress: compress})

			tmp := filepath.Join(filepath.Dir(path), "rotated-bytes")
			if err := os.WriteFile(tmp, []byte("precious\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The archive step cannot read the rotated bytes. The directory
			// stays writable, so removing the temp file would succeed —
			// only an early return keeps it.
			if err := os.Chmod(tmp, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(tmp, 0o644) })

			w.archive(tmp)

			if _, err := os.Stat(tmp); err != nil {
				t.Fatalf("temp file must survive a failed archive: %v", err)
			}
			backup := path + ".1"
			if compress {
				backup += ".gz"
			}
			if _, err := os.Stat(backup); !os.IsNotExist(err) {
				t.Errorf("a failed archive must not leave a backup at %s, err=%v", backup, err)
			}
		})
	}
}

// TestCloseIsIdempotent pins that a second Close is a no-op returning nil —
// cleanup functions get called from defer chains and signal handlers alike.
func TestCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	w, err := newRotatingWriter(path, RotateConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
	// Writes after Close are dropped, not errors: logging must never break
	// the program that is shutting down.
	n, err := w.Write([]byte("late\n"))
	if err != nil || n != len("late\n") {
		t.Errorf("Write after Close = %d, %v; want the bytes accepted and dropped", n, err)
	}
}

// TestGzipFile pins the compressor's contract, mirroring the copyFile test
// below: a missing source and an uncreatable destination are reported, a
// failed copy leaves no half-written .gz behind (a truncated archive would
// look like a valid backup to anything reading the directory), and a
// successful run round-trips the bytes.
func TestGzipFile(t *testing.T) {
	dir := t.TempDir()

	if err := gzipFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out.gz")); err == nil {
		t.Error("expected an error for a missing source")
	}

	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gzipFile(src, filepath.Join(dir, "no-such-dir", "out.gz")); err == nil {
		t.Error("expected an error for an uncreatable destination")
	}

	// A directory opens fine but cannot be read as a stream, so the copy
	// fails after the destination has been created.
	dst := filepath.Join(dir, "partial.gz")
	if err := gzipFile(dir, dst); err == nil {
		t.Error("expected an error copying from a directory")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a failed gzip must remove its partial output, err=%v", err)
	}

	// What it writes is readable gzip holding exactly the source bytes.
	big := filepath.Join(dir, "big")
	want := strings.Repeat("log line\n", 500)
	if err := os.WriteFile(big, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	round := filepath.Join(dir, "round.gz")
	if err := gzipFile(big, round); err != nil {
		t.Fatalf("gzipFile: %v", err)
	}
	f, err := os.Open(round)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	got, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("round trip lost data: %d bytes in, %d out", len(want), len(got))
	}
}

// TestCopyFileErrorsAndRoundTrip pins the plain-copy path used when
// compression is off.
func TestCopyFileErrorsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload\n" {
		t.Errorf("copy = %q, want the source contents", data)
	}

	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); err == nil {
		t.Error("expected an error for a missing source")
	}
	if err := copyFile(src, filepath.Join(dir, "no-such-dir", "x")); err == nil {
		t.Error("expected an error for an uncreatable destination")
	}
	if err := copyFile(dir, filepath.Join(dir, "fromdir")); err == nil {
		t.Error("expected an error copying from a directory")
	}
}

// assertNoRotTempFiles fails the test if a rotation temp file survived.
func assertNoRotTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "debug.log.rot-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("rotation temp files left behind: %v", matches)
	}
}
