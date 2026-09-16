package sdk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// useCacheDir points the extractor at dir for the duration of the test.
func useCacheDir(t *testing.T, dir string) {
	t.Helper()
	orig := cacheDir
	cacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheDir = orig })
}

// useSDKFS substitutes the tree that gets hashed and extracted.
func useSDKFS(t *testing.T, fsys fs.FS) {
	t.Helper()
	orig := sdkFS
	sdkFS = fsys
	t.Cleanup(func() { sdkFS = orig })
}

// errReadDirFS fails when the tree is listed — the shape a corrupted or
// unreadable SDK payload takes.
type errReadDirFS struct{ err error }

func (e errReadDirFS) Open(string) (fs.File, error)          { return nil, e.err }
func (e errReadDirFS) ReadDir(string) ([]fs.DirEntry, error) { return nil, e.err }
func (e errReadDirFS) ReadFile(string) ([]byte, error)       { return nil, e.err }

// errOpenFS lists files happily but fails to open one of them.
type errOpenFS struct {
	fstest.MapFS
	bad string
	err error
}

func (e errOpenFS) Open(name string) (fs.File, error) {
	if name == e.bad {
		return nil, e.err
	}
	return e.MapFS.Open(name)
}

// ReadFile must be overridden too: fs.ReadFile prefers an fs.ReadFileFS
// implementation, which MapFS provides, and would otherwise bypass Open.
func (e errOpenFS) ReadFile(name string) ([]byte, error) {
	if name == e.bad {
		return nil, e.err
	}
	return e.MapFS.ReadFile(name)
}

// hookFS runs a side effect once, after `after` file reads. EnsureExtracted
// walks the tree twice — once to hash it, once to extract it — so setting
// `after` to the file count fires the hook on the first read of the
// extraction pass. That is how the tests below simulate another fir process
// acting on the cache directory *mid-extraction*, which is the only window
// in which the rename can lose.
type hookFS struct {
	fstest.MapFS
	hook func()
	// failListing / failReads make every later directory listing or file
	// read fail; the hook sets one of them to simulate the tree becoming
	// unreadable mid-extraction.
	failListing bool
	failReads   bool
	after       int
	reads       int
	done        bool
}

func (h *hookFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if h.failListing {
		return nil, errors.New("listing failed mid-extraction")
	}
	return h.MapFS.ReadDir(name)
}

// newHookFS builds a hookFS that fires once extraction starts.
func newHookFS(tree fstest.MapFS, hook func()) *hookFS {
	return &hookFS{MapFS: tree, hook: hook, after: len(tree)}
}

func (h *hookFS) Open(name string) (fs.File, error) {
	f, err := h.MapFS.Open(name)
	h.fire(name, err)
	return f, err
}

// ReadFile is the path fs.ReadFile actually takes for a MapFS.
func (h *hookFS) ReadFile(name string) ([]byte, error) {
	if h.failReads {
		return nil, errors.New("read failed mid-extraction")
	}
	data, err := h.MapFS.ReadFile(name)
	h.fire(name, err)
	return data, err
}

func (h *hookFS) fire(name string, err error) {
	if err != nil || h.done || name == "." {
		return
	}
	h.reads++
	if h.reads <= h.after {
		return
	}
	h.done = true
	h.hook()
}

// sampleFS is a small stand-in SDK tree.
func sampleFS() fstest.MapFS {
	return fstest.MapFS{
		"python/fir_ext.py": &fstest.MapFile{Data: []byte("def run(): pass\n")},
		"python/helper.sh":  &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"node/index.js":     &fstest.MapFile{Data: []byte("module.exports={}\n")},
	}
}

// TestDefaultCacheDir pins where the SDKs land in production. Extensions
// resolve this path from their own environment, so it is a contract, not an
// implementation detail.
func TestDefaultCacheDir(t *testing.T) {
	// The platform cache root is consulted via pkg/cache, which honours
	// $XDG_CACHE_HOME when it is absolute. Pin that arm: it is the one
	// that behaves identically on every OS the suite runs on.
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)

	got, err := defaultCacheDir()
	if err != nil {
		t.Fatalf("defaultCacheDir: %v", err)
	}
	if want := filepath.Join(root, "fir", "sdks"); got != want {
		t.Errorf("defaultCacheDir = %q, want %q", got, want)
	}
}

// TestDefaultCacheDirNoCacheRoot pins that an unresolvable cache root is an
// error naming the cause, not an extraction into the working directory.
func TestDefaultCacheDirNoCacheRoot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")

	_, err := defaultCacheDir()
	if err == nil {
		t.Fatal("expected an error with no resolvable cache root")
	}
	if !strings.Contains(err.Error(), "resolve user cache dir") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestEnsureExtractedPropagatesCacheDirError pins that the failure surfaces
// from EnsureExtracted rather than being swallowed into a bad path.
func TestEnsureExtractedPropagatesCacheDirError(t *testing.T) {
	boom := errors.New("no cache dir")
	orig := cacheDir
	cacheDir = func() (string, error) { return "", boom }
	t.Cleanup(func() { cacheDir = orig })

	if _, err := EnsureExtracted(); !errors.Is(err, boom) {
		t.Fatalf("EnsureExtracted error = %v, want %v", err, boom)
	}
}

// TestExtractedFilePermissions pins that shell scripts come out executable
// and everything else does not — an extracted .sh that is not executable
// fails at exec time in the extension host, far from here.
func TestExtractedFilePermissions(t *testing.T) {
	useSDKFS(t, sampleFS())
	useCacheDir(t, t.TempDir())

	dir, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}

	sh, err := os.Stat(filepath.Join(dir, "python", "helper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if sh.Mode().Perm() != 0o755 {
		t.Errorf(".sh mode = %o, want 755", sh.Mode().Perm())
	}
	py, err := os.Stat(filepath.Join(dir, "python", "fir_ext.py"))
	if err != nil {
		t.Fatal(err)
	}
	if py.Mode().Perm() != 0o644 {
		t.Errorf(".py mode = %o, want 644", py.Mode().Perm())
	}
}

// TestHashTracksContentAndPaths pins the property the cache directory name
// relies on: a change to any file's contents *or* its path produces a
// different hash, so a stale extraction is never reused.
func TestHashTracksContentAndPaths(t *testing.T) {
	base := sampleFS()

	useSDKFS(t, base)
	original, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}

	changed := sampleFS()
	changed["python/fir_ext.py"] = &fstest.MapFile{Data: []byte("def run(): return 1\n")}
	useSDKFS(t, changed)
	afterEdit, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}
	if afterEdit == original {
		t.Error("editing a file did not change the hash")
	}

	renamed := sampleFS()
	renamed["python/fir_ext2.py"] = renamed["python/fir_ext.py"]
	delete(renamed, "python/fir_ext.py")
	useSDKFS(t, renamed)
	afterRename, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}
	if afterRename == original {
		t.Error("renaming a file did not change the hash — the path is not hashed")
	}
}

// TestHashReportsWalkFailure pins that an unreadable SDK tree is an error
// rather than a hash over whatever was readable, which would name a cache
// directory holding a partial SDK.
func TestHashReportsWalkFailure(t *testing.T) {
	boom := errors.New("listing failed")
	useSDKFS(t, errReadDirFS{err: boom})

	if _, err := embeddedHash(); !errors.Is(err, boom) {
		t.Fatalf("embeddedHash error = %v, want %v", err, boom)
	}

	useCacheDir(t, t.TempDir())
	_, err := EnsureExtracted()
	if err == nil || !strings.Contains(err.Error(), "sdk: hash embedded files") {
		t.Fatalf("EnsureExtracted error = %v, want it wrapped as a hash failure", err)
	}
}

// TestHashReportsReadFailure pins the same for a file that lists but cannot
// be read.
func TestHashReportsReadFailure(t *testing.T) {
	boom := errors.New("read failed")
	useSDKFS(t, errOpenFS{MapFS: sampleFS(), bad: "python/fir_ext.py", err: boom})

	if _, err := embeddedHash(); !errors.Is(err, boom) {
		t.Fatalf("embeddedHash error = %v, want %v", err, boom)
	}
}

// TestExtractReportsReadFailureAndCleansUp pins that a file which hashes
// cleanly but cannot be read while being copied out fails the extraction,
// names the file, and leaves no partial temp directory behind for the next
// run to trip over.
func TestExtractReportsReadFailureAndCleansUp(t *testing.T) {
	cache := t.TempDir()
	useCacheDir(t, cache)

	tree := sampleFS()
	useSDKFS(t, tree)
	hash, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}

	// Reads fail from the first extraction read onwards; hashing, which
	// walks the same tree first, still succeeds.
	hooked := newHookFS(tree, nil)
	hooked.hook = func() { hooked.failReads = true }
	useSDKFS(t, hooked)

	_, err = EnsureExtracted()
	if err == nil {
		t.Fatal("expected extraction to fail")
	}
	if !strings.Contains(err.Error(), "read embedded ") {
		t.Errorf("error = %v, want it to name the unreadable file", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, hash)); !os.IsNotExist(statErr) {
		t.Errorf("a failed run must not publish a cache directory, err=%v", statErr)
	}
	assertNoTempDirs(t, cache)
}

// TestExtractReportsWalkFailureDuringExtraction covers the extraction walk's
// own error passthrough, which is distinct from the hashing walk: the tree
// hashes cleanly and then becomes unreadable while being copied out.
func TestExtractReportsWalkFailureDuringExtraction(t *testing.T) {
	cache := t.TempDir()
	useCacheDir(t, cache)

	tree := sampleFS()
	hooked := newHookFS(tree, nil)
	hooked.hook = func() { hooked.failListing = true }
	useSDKFS(t, hooked)

	_, err := EnsureExtracted()
	if err == nil {
		t.Fatal("expected extraction to fail")
	}
	if !strings.Contains(err.Error(), "sdk: extract") {
		t.Errorf("error = %v, want it wrapped as an extract failure", err)
	}
	assertNoTempDirs(t, cache)
}

// TestExtractReportsUnwritableDestination pins that a temp tree corrupted
// under us (here: the subdirectory a file is being written into is replaced
// by a regular file) fails loudly instead of producing a partial SDK that
// would then be published under a hash claiming to be complete.
func TestExtractReportsUnwritableDestination(t *testing.T) {
	cache := t.TempDir()
	useCacheDir(t, cache)

	tree := sampleFS()
	hooked := newHookFS(tree, nil)
	hooked.hook = func() {
		// Fires just after the first extraction read (node/index.js) and
		// before its parent directory is ensured.
		tmp := soleTempDir(t, cache)
		victim := filepath.Join(tmp, "node")
		if err := os.RemoveAll(victim); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(victim, []byte("not a directory\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	useSDKFS(t, hooked)

	_, err := EnsureExtracted()
	if err == nil {
		t.Fatal("expected extraction to fail")
	}
	if !strings.Contains(err.Error(), "sdk: extract") {
		t.Errorf("error = %v, want it wrapped as an extract failure", err)
	}
	assertNoTempDirs(t, cache)
}

// TestExtractReportsUnwritableCache pins the two filesystem failures that a
// read-only cache root produces, both of which must name what fir was trying
// to do.
func TestExtractReportsUnwritableCache(t *testing.T) {
	useSDKFS(t, sampleFS())

	t.Run("cannot create the cache root", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(blocked, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		// The cache root cannot be created: its parent is a regular file.
		useCacheDir(t, filepath.Join(blocked, "sdks"))

		_, err := EnsureExtracted()
		if err == nil || !strings.Contains(err.Error(), "sdk: mkdir cache") {
			t.Fatalf("error = %v, want a mkdir-cache failure", err)
		}
	})

	t.Run("cannot create the temp directory", func(t *testing.T) {
		cache := t.TempDir()
		if err := os.Chmod(cache, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cache, 0o700) })
		useCacheDir(t, cache)

		_, err := EnsureExtracted()
		if err == nil || !strings.Contains(err.Error(), "sdk: create temp dir") {
			t.Fatalf("error = %v, want a temp-dir failure", err)
		}
	})
}

// TestExtractLosesRenameRaceToAnotherProcess pins the documented concurrency
// contract: when a second fir process publishes the same content-addressed
// directory while we are extracting, we adopt its copy instead of failing —
// and we must not delete it on the way out.
func TestExtractLosesRenameRaceToAnotherProcess(t *testing.T) {
	cache := t.TempDir()
	useCacheDir(t, cache)

	tree := sampleFS()
	useSDKFS(t, tree)
	hash, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}
	winner := filepath.Join(cache, hash)

	// Mid-extraction, "another process" publishes a non-empty directory at
	// exactly our destination, so our rename fails with ENOTEMPTY.
	hooked := newHookFS(tree, func() {
		if err := os.MkdirAll(filepath.Join(winner, "python"), 0o755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(winner, "python", "fir_ext.py"), []byte("theirs\n"), 0o644); err != nil {
			t.Error(err)
		}
	})
	useSDKFS(t, hooked)

	got, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("losing the race must not be an error: %v", err)
	}
	if got != winner {
		t.Fatalf("EnsureExtracted = %q, want the winner's directory %q", got, winner)
	}
	data, err := os.ReadFile(filepath.Join(winner, "python", "fir_ext.py"))
	if err != nil {
		t.Fatalf("the winner's copy must survive: %v", err)
	}
	if string(data) != "theirs\n" {
		t.Errorf("winner's file was overwritten: %q", data)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".extract-") {
			t.Errorf("temp directory %s left behind after losing the race", e.Name())
		}
	}
}

// TestExtractReportsRenameFailure pins the other arm: a rename that fails
// with nothing at the destination is a real error, not a silent success.
func TestExtractReportsRenameFailure(t *testing.T) {
	cache := t.TempDir()
	useCacheDir(t, cache)

	tree := sampleFS()
	useSDKFS(t, tree)
	hash, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}

	// Mid-extraction the cache root becomes read-only, so the publish
	// rename cannot happen and no destination directory exists.
	hooked := newHookFS(tree, func() {
		if err := os.Chmod(cache, 0o500); err != nil {
			t.Error(err)
		}
	})
	useSDKFS(t, hooked)
	t.Cleanup(func() { _ = os.Chmod(cache, 0o700) })

	_, err = EnsureExtracted()
	if err == nil {
		t.Fatal("expected the publish rename to fail")
	}
	if !strings.Contains(err.Error(), "sdk: rename") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, hash)); !os.IsNotExist(statErr) {
		t.Errorf("no cache directory should have been published, err=%v", statErr)
	}
}

// soleTempDir returns the single .extract-* directory under cache, failing
// the test if there is not exactly one.
func soleTempDir(t *testing.T, cache string) string {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".extract-") {
			found = append(found, filepath.Join(cache, e.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one temp dir under %s, got %v", cache, found)
	}
	return found[0]
}

// assertNoTempDirs pins the cleanup contract: a failed or losing run leaves
// nothing behind for the next run to trip over.
func assertNoTempDirs(t *testing.T, cache string) {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".extract-") {
			t.Errorf("temp directory %s left behind", e.Name())
		}
	}
}

// TestPrependEnvKeepsExistingValue pins that fir prepends to an inherited
// PYTHONPATH rather than replacing it — an extension that depends on a
// user-installed package must still import it.
func TestPrependEnvKeepsExistingValue(t *testing.T) {
	t.Setenv("PYTHONPATH", "/user/site-packages")
	t.Setenv("NODE_PATH", "")

	env := SDKEnv("/base")

	if want := "PYTHONPATH=/base/python:/user/site-packages"; env[0] != want {
		t.Errorf("env[0] = %q, want %q", env[0], want)
	}
	if want := "NODE_PATH=/base/node"; env[1] != want {
		t.Errorf("env[1] = %q, want %q", env[1], want)
	}
}
