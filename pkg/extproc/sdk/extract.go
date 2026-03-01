package sdk

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SDKVersion is bumped whenever the embedded SDK files change.
// A new version causes re-extraction on next run.
const SDKVersion = "1"

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

// EnsureExtracted extracts the embedded SDK files to
// ~/.cache/fir/sdks/<version>/ if they are not already present.
// It returns the base path (e.g. ~/.cache/fir/sdks/1/).
func EnsureExtracted() (string, error) {
	base, err := cacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, SDKVersion)

	// Marker file signals a complete extraction.
	marker := filepath.Join(dir, ".extracted")
	if _, err := os.Stat(marker); err == nil {
		return dir, nil
	}

	// Walk the embedded FS and write every file.
	if err := fs.WalkDir(EmbeddedSDKs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, path)
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
		return os.WriteFile(dest, data, 0o644)
	}); err != nil {
		return "", fmt.Errorf("sdk: extract: %w", err)
	}

	// Write marker.
	if err := os.WriteFile(marker, []byte(SDKVersion), 0o644); err != nil {
		return "", fmt.Errorf("sdk: write marker: %w", err)
	}

	return dir, nil
}
