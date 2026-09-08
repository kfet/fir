// Package cache owns fir's on-disk cache location and the extraction
// lifecycle shared by everything fir materialises out of its own binary:
// builtin skills, builtin extensions, and the extension SDKs.
//
// Each of those grew its own answer to the same two questions — where does
// an extracted tree live, and who deletes it — and got a different one:
// two hardcoded `~/.cache` paths, one $TMPDIR path, and no collection
// anywhere. This package is the single answer.
package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Indirected for tests: one so a test never touches a real $HOME, one so
// the "entry vanished under us" race in SweepAged is reachable without
// actually racing.
var (
	homeDir      = os.UserHomeDir
	userCacheDir = os.UserCacheDir
	statDir      = os.Stat
)

const (
	// MaxAge is how long an extracted tree survives without being claimed
	// by a starting process before it is collected. It has to outlast the
	// longest plausible live session still holding paths into an older
	// tree — those paths are absolute, and the process holding them will
	// never re-resolve them.
	MaxAge = 14 * 24 * time.Hour
	// LegacyTmpMaxAge applies to the $TMPDIR locations earlier fir
	// versions extracted into and no current fir creates. Temp dirs are
	// disposable by definition and the OS may purge them anyway, so this
	// is deliberately shorter.
	LegacyTmpMaxAge = 3 * 24 * time.Hour
)

// Dir returns the per-OS user cache directory joined with fir/<sub>:
//
//	Linux/BSD   $XDG_CACHE_HOME/fir/<sub>, else ~/.cache/fir/<sub>
//	macOS       ~/Library/Caches/fir/<sub>
//	Windows     %LocalAppData%/fir/<sub>
//
// via os.UserCacheDir, which is where the per-platform convention
// actually lives. ~/.cache is the XDG Base Directory answer and it is
// canonical on Linux only; Apple's cache directory is ~/Library/Caches
// (NSCachesDirectory), and hardcoding ~/.cache put fir's caches in a
// non-standard place on every Mac it ran on.
//
// $XDG_CACHE_HOME is honoured on ALL platforms when set to an absolute
// path, which is one step beyond os.UserCacheDir (it consults the
// variable on unix only). A user who exports it has said where caches
// go, and a CLI that ignores that on macOS is second-guessing an
// explicit instruction — the same reasoning by which fir already
// honours $XDG_CONFIG_HOME for its agent dir on every platform.
func Dir(sub string) (string, error) {
	if xdg := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "fir", sub), nil
	}
	base, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("cache: resolve user cache dir: %w", err)
	}
	return filepath.Join(base, "fir", sub), nil
}

// legacyDotCacheDir is ~/.cache/fir/<sub> — the hardcoded location fir
// used before Dir consulted the platform. It is returned only when it
// differs from the current Dir(sub), i.e. exactly when there is
// something to migrate away from: a Mac, or a host that set
// $XDG_CACHE_HOME somewhere else.
func legacyDotCacheDir(sub string) (string, bool) {
	home, err := homeDir()
	if err != nil {
		return "", false
	}
	legacy := filepath.Join(home, ".cache", "fir", sub)
	current, err := Dir(sub)
	if err != nil || current == legacy {
		return "", false
	}
	return legacy, true
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

// SweepDir ages out every tree under one abandoned location and removes
// the location itself once it is empty. Used for the paths fir no longer
// extracts into: the $TMPDIR bases of older versions, and ~/.cache on
// platforms where that is not the OS's cache directory.
func SweepDir(base string, maxAge time.Duration) {
	SweepAged(base, "", maxAge, NotDotted)
	// Fails while the location still holds a young tree, which is the
	// intended outcome — it is retried on the next run.
	_ = os.Remove(base)
}

// SweepLegacy collects the locations fir used to extract <sub> into: the
// pre-platform ~/.cache path (a no-op where that is still the real cache
// dir) and any $TMPDIR bases passed by the caller.
func SweepLegacy(sub string, tmpBases ...string) {
	if legacy, ok := legacyDotCacheDir(sub); ok {
		SweepDir(legacy, MaxAge)
	}
	for _, base := range tmpBases {
		SweepDir(base, LegacyTmpMaxAge)
	}
}

// NotDotted matches any name that is not a dotfile. It is the usual
// match for a cache dir of content-addressed trees, whose in-progress
// extractions are named `.extract-*`.
func NotDotted(name string) bool { return len(name) > 0 && name[0] != '.' }
