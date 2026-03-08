// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 4ba3e5be

//go:build !windows

package auth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"os/exec"
)

// TestFileLockHelper is a subprocess helper used by
// TestFileAuthStorageBackend_WithLock_CrossProcess. It must not be run
// directly (it skips itself unless the GO_TEST_FLOCK_HELPER env var is set).
func TestFileLockHelper(t *testing.T) {
	if os.Getenv("GO_TEST_FLOCK_HELPER") != "1" {
		t.Skip("subprocess helper: only runs when GO_TEST_FLOCK_HELPER=1")
	}
	dir := os.Getenv("GO_TEST_FLOCK_DIR")
	if dir == "" {
		t.Fatal("GO_TEST_FLOCK_DIR not set")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "auth.json.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer f.Close()
	if err := flockExclusive(int(f.Fd())); err != nil {
		t.Fatalf("flock: %v", err)
	}
	// Signal parent that we hold the lock.
	fmt.Fprintln(os.Stdout, "LOCKED")
	os.Stdout.Sync() //nolint:errcheck
	// Block until parent closes our stdin (signals us to release).
	bufio.NewReader(os.Stdin).ReadByte() //nolint:errcheck
}

// TestFileAuthStorageBackend_WithLock_CrossProcess verifies that
// WithLock blocks a second process while the first holds the flock.
func TestFileAuthStorageBackend_WithLock_CrossProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	// Spawn ourselves as a subprocess helper that holds the lock.
	cmd := exec.Command(os.Args[0],
		"-test.run=TestFileLockHelper",
		"-test.v",
	)
	cmd.Env = append(os.Environ(),
		"GO_TEST_FLOCK_HELPER=1",
		"GO_TEST_FLOCK_DIR="+dir,
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		stdin.Close()
		cmd.Wait() //nolint:errcheck
	}()

	// Wait for the subprocess to acquire the lock.
	locked := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "LOCKED" {
				close(locked)
				return
			}
		}
	}()
	select {
	case <-locked:
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess did not signal LOCKED within timeout")
	}

	// Now attempt WithLock — it should block because the subprocess holds the lock.
	backend := NewFileAuthStorageBackend(path)
	done := make(chan error, 1)
	go func() {
		_, err := backend.WithLock(func(_ []byte) (any, []byte) { return nil, nil })
		done <- err
	}()

	// Verify WithLock is blocked for at least 100 ms.
	select {
	case err := <-done:
		t.Fatalf("WithLock returned immediately (%v) — expected it to block while subprocess holds lock", err)
	case <-time.After(100 * time.Millisecond):
		// Good: still blocking.
	}

	// Release the subprocess lock by closing its stdin.
	if err := stdin.Close(); err != nil {
		t.Fatalf("close helper stdin: %v", err)
	}

	// WithLock should now complete.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock returned error after lock released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WithLock did not complete after lock was released")
	}
}
