package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// jsEntryBasenames are the conventional entry-point stems for a JS/TS
// extension. A file qualifies as an entry point if its stem is one of these,
// or equals its containing directory's name (e.g. foo/foo.ts).
var jsEntryBasenames = map[string]bool{"index": true, "main": true}

// jsWrapperSkipDirs are directory names never descended into when scanning a
// package for JS/TS entry points.
var jsWrapperSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	"test": true, "tests": true, "dist": true, "build": true,
}

// GenerateJSWrappers scans an installed package directory for JS/TS extension
// entry points and creates a `main → run.sh` symlink next to each one. The
// `main` symlink is an extensionless executable entry point, so package
// auto-discovery (ScanPackageResources -> ConfigsFromFiles) collects it and
// names the extension after its directory — the same convention used for a
// `.fir/extensions/<name>/main` entry point.
//
// This closes the discovery gap for "pi-style" JS/TS packages: previously a
// fresh session loaded zero extensions because package discovery only collected
// frontmatter-bearing `.py`/`.sh` files. Generation lives in core (not the
// install extension) so every install path — the `fir install` CLI verb and
// the `/install` slash-command alike — produces a loadable package.
//
// It returns the number of symlinks created (a valid `main` is left untouched,
// making this idempotent; a dangling one is repaired). A missing JS/TS runtime
// SDK or a package with no JS/TS entry points is not an error — it returns
// 0, nil.

func GenerateJSWrappers(pkgDir string) (int, error) {
	runSh, err := jsRunShPath()
	if err != nil || runSh == "" {
		// No SDK runtime available — nothing to wrap. Not fatal.
		return 0, nil
	}

	dirs, err := findJSTSEntryDirs(pkgDir)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, d := range dirs {
		ok, err := makeJSWrapper(d, runSh)
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}

// jsRunShPath returns the absolute path to the SDK's generic JS/TS runtime
// wrapper (run.sh), extracting the embedded SDK if necessary.
func jsRunShPath() (string, error) {
	base, err := sdk.EnsureExtracted()
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, "node", "run.sh")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// findJSTSEntryDirs returns the set of directories that contain a JS/TS
// extension entry point. Only conventional entry-point filenames
// (index/main/<dirname>.ts|js, without a shebang) qualify, so a directory of
// helper modules never gets a wrapper. The walk descends from the package root
// only into `extensions`/`ext` subdirectories (mirroring fir's extension
// layout) to avoid wrapping arbitrary source trees.
func findJSTSEntryDirs(pkgDir string) ([]string, error) {
	seen := make(map[string]bool)
	var dirs []string

	add := func(dir string) {
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}

	walk := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil // skip unreadable dirs
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".ts" && ext != ".js" {
				continue
			}
			if strings.HasSuffix(name, ".d.ts") {
				continue
			}
			stem := strings.TrimSuffix(name, filepath.Ext(name))
			if !jsEntryBasenames[stem] && stem != filepath.Base(dir) {
				continue
			}
			if fileHasShebang(filepath.Join(dir, name)) {
				continue
			}
			add(dir)
		}
		return nil
	}

	// Root level.
	if err := walk(pkgDir); err != nil {
		return nil, err
	}

	// Descend only through extensions/ext subdirectories (recursively),
	// matching install.py's historical behaviour.
	var descend func(dir string) error
	descend = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if jsWrapperSkipDirs[e.Name()] {
				continue
			}
			if e.Name() == "extensions" || e.Name() == "ext" {
				sub := filepath.Join(dir, e.Name())
				if err := walk(sub); err != nil {
					return err
				}
				if err := descend(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := descend(pkgDir); err != nil {
		return nil, err
	}

	return dirs, nil
}

// fileHasShebang reports whether path begins with "#!".
func fileHasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 2)
	n, _ := f.Read(buf)
	return n == 2 && buf[0] == '#' && buf[1] == '!'
}

// makeJSWrapper creates a `main → run.sh` symlink in extDir so the JS/TS
// extension is discovered through the same extensionless entry-point convention
// as a `.fir/extensions/<name>/main` binary: package discovery collects the
// `main` entry and names the extension after extDir. When spawned, run.sh
// resolves its own SDK directory (for pi_compat.js) and discovers the entry
// point from the symlink's directory ($0).
//
// Returns false (no error) if a usable `main` already exists. A dangling
// symlink (target gone after an SDK-cache change) is repaired.
func makeJSWrapper(extDir, runSh string) (bool, error) {
	link := filepath.Join(extDir, "main")
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if _, serr := os.Stat(link); serr == nil {
				return false, nil // valid symlink already present
			}
			// Dangling symlink — repair it.
			if rerr := os.Remove(link); rerr != nil {
				return false, fmt.Errorf("removing stale main symlink %s: %w", link, rerr)
			}
		} else {
			// A real file/dir named `main` already exists — leave it alone.
			return false, nil
		}
	}
	if err := os.Symlink(runSh, link); err != nil {
		return false, fmt.Errorf("symlinking %s → %s: %w", link, runSh, err)
	}
	return true, nil
}
