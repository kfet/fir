package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// cacheDir is the function used to locate the cache directory.
// Tests override this to avoid touching ~/.cache.
var cacheDir = defaultCacheDir

func defaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sdk: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "fir", "sdks"), nil
}

// embeddedHash returns a deterministic hash of all embedded SDK files.
// Used as the directory name so extraction is skipped when unchanged.
func embeddedHash() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(EmbeddedSDKs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Include path in hash so renames are detected.
		h.Write([]byte(path))
		if d.IsDir() {
			return nil
		}
		data, err := EmbeddedSDKs.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// EnsureExtracted extracts the embedded SDK files to a content-addressed
// directory under ~/.cache/fir/sdks/<hash>/. The hash is derived from the
// embedded file contents so extraction is skipped when the SDK hasn't changed.
//
// Extraction is atomic: files are written to a temp directory and renamed
// into place, so concurrent fir processes never see a partial extraction.
func EnsureExtracted() (string, error) {
	base, err := cacheDir()
	if err != nil {
		return "", err
	}

	hash, err := embeddedHash()
	if err != nil {
		return "", fmt.Errorf("sdk: hash embedded files: %w", err)
	}

	dir := filepath.Join(base, hash)

	// If the directory already exists, the SDK is up to date.
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	// Extract to a temp directory in the same parent, then rename atomically.
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("sdk: mkdir cache: %w", err)
	}
	tmp, err := os.MkdirTemp(base, ".extract-")
	if err != nil {
		return "", fmt.Errorf("sdk: create temp dir: %w", err)
	}

	// Clean up temp dir on failure; on success it's been renamed away.
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tmp)
		}
	}()

	if err := fs.WalkDir(EmbeddedSDKs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(tmp, path)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := EmbeddedSDKs.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if filepath.Ext(path) == ".sh" {
			perm = 0o755
		}
		return os.WriteFile(dest, data, perm)
	}); err != nil {
		return "", fmt.Errorf("sdk: extract: %w", err)
	}

	// Atomic rename. If another process raced us, one rename wins and the
	// loser gets EEXIST/ENOTEMPTY — that's fine, the content is identical.
	if err := os.Rename(tmp, dir); err != nil {
		// Another process won the race — use their copy.
		if _, statErr := os.Stat(dir); statErr == nil {
			success = true // prevent cleanup of already-renamed dir
			return dir, nil
		}
		return "", fmt.Errorf("sdk: rename: %w", err)
	}
	success = true

	return dir, nil
}
