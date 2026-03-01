package extproc

import (
	"os"
	"path/filepath"
	"strings"
)

// ExtProcConfig describes a discovered external process extension.
type ExtProcConfig struct {
	Name  string // derived from filename (sans extension)
	Path  string // absolute path to the executable
	Scope string // "project" or "global"
}

// Discover scans global (~/.config/fir/extensions/) and project-local
// (.fir/extensions/) directories for executable files. Project-local
// extensions shadow global ones by name.
func Discover(projectDir string) ([]ExtProcConfig, error) {
	byName := make(map[string]ExtProcConfig)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	globalDir := filepath.Join(homeDir, ".config", "fir", "extensions")

	// Global first, then project-local (shadows global).
	dirs := []struct {
		path  string
		scope string
	}{
		{globalDir, "global"},
		{filepath.Join(projectDir, ".fir", "extensions"), "project"},
	}

	for _, d := range dirs {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0111 == 0 {
				continue
			}
			name := stripExt(e.Name())
			byName[name] = ExtProcConfig{
				Name:  name,
				Path:  filepath.Join(d.path, e.Name()),
				Scope: d.scope,
			}
		}
	}

	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result, nil
}

func stripExt(name string) string {
	ext := filepath.Ext(name)
	if ext != "" {
		return strings.TrimSuffix(name, ext)
	}
	return name
}
