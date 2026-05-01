package extension

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// ExtProcConfig describes a discovered external process extension.
type ExtProcConfig struct {
	Name          string   // derived from filename or sub-directory name
	Path          string   // absolute path to the executable
	Scope         string   // "project", "global", or "builtin"
	Modes         []string // optional mode allowlist from comment frontmatter
	AuthProviders []string // auth provider IDs declared in frontmatter
	CLIVerbs      []string // top-level `fir <verb>` names declared in frontmatter
}

// Discover scans global (~/.config/fir/extensions/) and project-local
// (.fir/extensions/) directories for executable files and sub-directories.
// Project-local extensions shadow global ones by name.
//
// Sub-directory support: if an entry is a directory, the directory name
// becomes the extension name and the entry point is resolved by looking for
// (in order): main, main.py, main.sh, <dirname>, <dirname>.py, <dirname>.sh,
// or the first executable file found alphabetically.
func Discover(projectDir string) ([]ExtProcConfig, error) {
	byName := make(map[string]ExtProcConfig)

	// Builtin extensions (lowest priority — shadowed by global and project).
	builtins, err := resources.LoadBuiltinExtensions()
	if err == nil {
		for _, b := range builtins {
			fm := extensionFrontmatterFromPath(b.Path)
			byName[b.Name] = ExtProcConfig{
				Name:          b.Name,
				Path:          b.Path,
				Scope:         "builtin",
				Modes:         fm.Modes,
				AuthProviders: fm.AuthProviders,
				CLIVerbs:      fm.CLIVerbs,
			}
		}
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	globalDir := filepath.Join(homeDir, ".config", "fir", "extensions")

	// Respect $XDG_CONFIG_HOME if set.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		globalDir = filepath.Join(xdg, "fir", "extensions")
	}

	// Global then project-local (each shadows the previous).
	dirs := []struct {
		path  string
		scope string
	}{
		{globalDir, "global"},
		{filepath.Join(projectDir, ".fir", "extensions"), "project"},
	}

	for _, d := range dirs {
		if err := scanExtDir(d.path, d.scope, byName); err != nil {
			return nil, err
		}
	}

	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result, nil
}

// DiscoverWithDirs is like Discover but accepts explicit global and project
// extension directories. This is useful for testing without depending on
// os.UserHomeDir.
func DiscoverWithDirs(globalDir, projectExtDir string) ([]ExtProcConfig, error) {
	byName := make(map[string]ExtProcConfig)

	dirs := []struct {
		path  string
		scope string
	}{
		{globalDir, "global"},
		{projectExtDir, "project"},
	}

	for _, d := range dirs {
		if err := scanExtDir(d.path, d.scope, byName); err != nil {
			return nil, err
		}
	}

	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result, nil
}

// DiscoverExtra scans a list of additional directories for extension scripts,
// returning configs with scope "package". Package extensions are lowest
// priority — they are shadowed by global and project extensions.
// Use this to load extensions contributed by installed fir packages.
func DiscoverExtra(dirs []string) ([]ExtProcConfig, error) {
	byName := make(map[string]ExtProcConfig)
	for _, dir := range dirs {
		if err := scanExtDir(dir, "package", byName); err != nil {
			return nil, err
		}
	}
	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result, nil
}

// ConfigsFromFiles builds extension configs from individual script file paths.
// Each file must be executable and have valid comment frontmatter to be included.
// Files that fail these checks are silently skipped.
// The resulting configs have scope "package" and are intended to be merged at
// lower priority than global/project extensions.
func ConfigsFromFiles(files []string) []ExtProcConfig {
	byName := make(map[string]ExtProcConfig, len(files))
	for _, filePath := range files {
		if !isExecutableFile(filePath) {
			continue
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		fm := resources.ParseCommentFrontmatter(string(data))
		if !fm.Present || fm.Builtin {
			continue
		}
		name := stripExt(filepath.Base(filePath))
		byName[name] = ExtProcConfig{
			Name:          name,
			Path:          filePath,
			Scope:         "package",
			Modes:         fm.Modes,
			AuthProviders: fm.AuthProviders,
			CLIVerbs:      fm.CLIVerbs,
		}
	}
	result := make([]ExtProcConfig, 0, len(byName))
	for _, cfg := range byName {
		result = append(result, cfg)
	}
	return result
}

// scanExtDir scans a single extensions directory, populating byName.
// It handles both plain executable files and sub-directories.
// Files with builtin: true comment frontmatter are skipped when scanning
// project or global directories (they are handled by LoadBuiltinExtensions).
func scanExtDir(dir, scope string, byName map[string]ExtProcConfig) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			// Sub-directory: directory name is the extension name.
			name := e.Name()
			subdir := filepath.Join(dir, name)
			entryPoint, ok := findSubdirEntryPoint(subdir, name)
			if !ok {
				continue
			}
			fm := extensionFrontmatterFromPath(entryPoint)
			byName[name] = ExtProcConfig{
				Name:          name,
				Path:          entryPoint,
				Scope:         scope,
				Modes:         fm.Modes,
				AuthProviders: fm.AuthProviders,
				CLIVerbs:      fm.CLIVerbs,
			}
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
		filePath := filepath.Join(dir, e.Name())

		// Parse comment frontmatter once so we can skip builtin files and capture mode constraints.
		// Files without valid frontmatter (e.g. test scripts, helper modules) are not extensions.
		fm := resources.ExtensionFrontmatter{}
		if data, err := os.ReadFile(filePath); err == nil {
			fm = resources.ParseCommentFrontmatter(string(data))
			if !fm.Present {
				continue
			}
			if fm.Builtin {
				continue
			}
		}

		byName[name] = ExtProcConfig{
			Name:          name,
			Path:          filePath,
			Scope:         scope,
			Modes:         fm.Modes,
			AuthProviders: fm.AuthProviders,
			CLIVerbs:      fm.CLIVerbs,
		}
	}
	return nil
}

// findSubdirEntryPoint searches a sub-directory for an executable entry point.
// It checks (in order): main, main.py, main.sh, <dirname>, <dirname>.py,
// <dirname>.sh, then falls back to the first executable found alphabetically.
// Returns the absolute path and true if found.
func findSubdirEntryPoint(subdir, dirname string) (string, bool) {
	candidates := []string{
		"main",
		"main.py",
		"main.sh",
		dirname,
		dirname + ".py",
		dirname + ".sh",
	}
	for _, c := range candidates {
		p := filepath.Join(subdir, c)
		if isExecutableFile(p) {
			return p, true
		}
	}

	// Fallback: first executable file alphabetically.
	entries, err := os.ReadDir(subdir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(subdir, e.Name())
		if isExecutableFile(p) {
			return p, true
		}
	}
	return "", false
}

// isExecutableFile returns true if path exists, is a regular file, and is
// executable by at least one permission class (user/group/other).
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// stripExt removes a single trailing file extension (e.g. ".py", ".sh").
// This intentionally strips only one extension: "foo.tar.gz" becomes "foo.tar".
// For extension naming this is sufficient since executable extensions use
// single extensions like .py, .sh, or no extension at all.
func stripExt(name string) string {
	ext := filepath.Ext(name)
	if ext != "" {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

func extensionFrontmatterFromPath(path string) resources.ExtensionFrontmatter {
	data, err := os.ReadFile(path)
	if err != nil {
		return resources.ExtensionFrontmatter{}
	}
	return resources.ParseCommentFrontmatter(string(data))
}
