// Package cache owns fir's on-disk cache location and the extraction
// lifecycle shared by everything fir materialises out of its own binary:
// builtin skills, builtin extensions, and the extension SDKs.
//
// Each of those grew its own answer to the same two questions — where does
// an extracted tree live, and who deletes it — and got a different one:
// two hardcoded `~/.cache` paths that ignored $XDG_CACHE_HOME, one
// $TMPDIR path, and no collection anywhere. This package is the single
// answer.
package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// homeDir and statDir are indirected for tests: one so a test never
// touches a real $HOME, the other so the "entry vanished under us" race
// in SweepAged is reachable without actually racing.
var (
	homeDir = os.UserHomeDir
	statDir = os.Stat
)

// Dir returns <cache>/fir/<sub>, where <cache> is $XDG_CACHE_HOME when
// set and ~/.cache otherwise.
//
// $XDG_CACHE_HOME is honoured because fir already honours
// $XDG_CONFIG_HOME for its agent dir (session.DefaultAgentDir,
// mcp.defaultConfigDir). Respecting one half of the spec and hardcoding
// the other is not a policy, it is an oversight — on a host that sets
// both, fir read its config from where it was told and then wrote
// hundreds of megabytes to where it was not.
func Dir(sub string) (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "fir", sub), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", fmt.Errorf("cache: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "fir", sub), nil
}

// Claim marks dir as in use by this process by refreshing its mtime, so
// that SweepAged treats "old" as "not claimed by any recent process"
// rather than "extracted long ago". Best-effort: a cache directory on a
// read-only or exotic filesystem is still perfectly usable.
func Claim(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// SweepAged removes immediate subdirectories of base that match, are
// older than maxAge, and are not keep.
//
// Age, not exactness, is the safety property. An extracted tree is
// content-addressed, so a *running* process started by an older binary
// still holds absolute paths into a directory the current binary will
// never choose — deleting it on sight breaks a live session's skill
// loads or extension launches. Anything untouched for maxAge belongs to
// a process that has long since exited.
//
// Best-effort throughout: an unreadable cache dir or an undeletable
// entry is not worth failing a session over.
func SweepAged(base, keep string, maxAge time.Duration, match func(name string) bool) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() || !match(e.Name()) {
			continue
		}
		path := filepath.Join(base, e.Name())
		if path == keep {
			continue
		}
		info, err := statDir(path)
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.RemoveAll(path)
	}
}

// NotDotted matches any name that is not a dotfile. It is the usual
// match for a cache dir of content-addressed trees, whose in-progress
// extractions are named `.extract-*`.
func NotDotted(name string) bool { return len(name) > 0 && name[0] != '.' }
