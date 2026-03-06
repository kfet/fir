package core

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed builtin_extensions
var BuiltinExtensionsFS embed.FS

var (
	builtinExtExtractOnce sync.Once
	builtinExtExtractDir  string
	builtinExtExtractErr  error
)

// ExtensionFrontmatter holds metadata parsed from a comment frontmatter block
// at the top of an extension script.
type ExtensionFrontmatter struct {
	Name        string
	Description string
	Builtin     bool
	Modes       []string
}

// ParseCommentFrontmatter parses frontmatter from comment-delimited blocks.
// It looks for a "# ---" opening and closing delimiter, stripping the comment
// prefix from each line before parsing key: value pairs.
//
// Example:
//
//	# ---
//	# name: my-ext
//	# builtin: true
//	# ---
func ParseCommentFrontmatter(content string) ExtensionFrontmatter {
	var fm ExtensionFrontmatter

	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return fm
	}

	// Skip shebang line if present.
	start := 0
	if strings.HasPrefix(lines[0], "#!") {
		start = 1
	}

	// Find opening "# ---"
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "# ---" {
		return fm
	}

	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "# ---" {
			// Closing delimiter found; return what we have.
			return fm
		}
		if !strings.HasPrefix(line, "# ") {
			// Not a comment line — invalid frontmatter.
			return ExtensionFrontmatter{}
		}
		kv := strings.TrimPrefix(line, "# ")
		key, value, ok := strings.Cut(kv, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			fm.Name = value
		case "description":
			fm.Description = value
		case "builtin":
			fm.Builtin = value == "true"
		case "mode", "modes":
			fm.Modes = parseExtensionModes(value)
		}
	}

	// No closing delimiter found.
	return ExtensionFrontmatter{}
}

func parseExtensionModes(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	modes := make([]string, 0, len(parts))
	for _, part := range parts {
		mode := strings.Trim(strings.TrimSpace(part), `"'`)
		if mode == "" {
			continue
		}
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return nil
	}
	return modes
}

// extractBuiltinExtensions extracts the builtin_extensions/ tree to a temp
// directory so extension scripts can be executed as subprocesses.
func extractBuiltinExtensions() (string, error) {
	builtinExtExtractOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fir-builtin-extensions-")
		if err != nil {
			builtinExtExtractErr = fmt.Errorf("create temp dir for builtin extensions: %w", err)
			return
		}
		builtinExtExtractDir = dir

		err = fs.WalkDir(BuiltinExtensionsFS, "builtin_extensions", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, "builtin_extensions/")
			if rel == "" || path == "builtin_extensions" {
				return nil
			}
			target := filepath.Join(dir, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			data, err := BuiltinExtensionsFS.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, 0o755); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			builtinExtExtractErr = fmt.Errorf("extract builtin extensions: %w", err)
		}
	})
	return builtinExtExtractDir, builtinExtExtractErr
}

// BuiltinExtension describes a builtin extension discovered from the embedded FS.
type BuiltinExtension struct {
	Name string
	Path string // absolute path to the extracted executable
}

// LoadBuiltinExtensions returns extensions marked with builtin: true in their
// comment frontmatter. The scripts are extracted to a temp directory so they
// can be executed as subprocesses.
func LoadBuiltinExtensions() ([]BuiltinExtension, error) {
	extractDir, err := extractBuiltinExtensions()
	if err != nil {
		return nil, err
	}

	var extensions []BuiltinExtension

	entries, err := BuiltinExtensionsFS.ReadDir("builtin_extensions")
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			exts, err := loadBuiltinSubdirExtension(extractDir, e.Name())
			if err != nil {
				continue
			}
			extensions = append(extensions, exts...)
			continue
		}
		path := "builtin_extensions/" + e.Name()
		data, err := BuiltinExtensionsFS.ReadFile(path)
		if err != nil {
			continue
		}
		fm := ParseCommentFrontmatter(string(data))
		if !fm.Builtin {
			continue
		}
		name := fm.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		extensions = append(extensions, BuiltinExtension{
			Name: name,
			Path: filepath.Join(extractDir, e.Name()),
		})
	}

	return extensions, nil
}

// loadBuiltinSubdirExtension loads a builtin extension from a subdirectory in
// the embedded FS. It resolves the entry point using the same candidate list as
// project-level subdirectory extensions: main.py, main.sh, main, <dirname>.py,
// <dirname>.sh, <dirname>, then the first file alphabetically.
func loadBuiltinSubdirExtension(extractDir, dirname string) ([]BuiltinExtension, error) {
	fsDir := "builtin_extensions/" + dirname

	entryName, err := findBuiltinSubdirEntryPoint(fsDir, dirname)
	if err != nil {
		return nil, err
	}

	data, err := BuiltinExtensionsFS.ReadFile(fsDir + "/" + entryName)
	if err != nil {
		return nil, err
	}

	fm := ParseCommentFrontmatter(string(data))
	if !fm.Builtin {
		return nil, nil
	}

	name := fm.Name
	if name == "" {
		name = dirname
	}

	return []BuiltinExtension{{
		Name: name,
		Path: filepath.Join(extractDir, dirname, entryName),
	}}, nil
}

// findBuiltinSubdirEntryPoint returns the filename of the entry point inside
// fsDir (an embedded FS path). Checks candidates in order, then falls back to
// the first file alphabetically.
func findBuiltinSubdirEntryPoint(fsDir, dirname string) (string, error) {
	candidates := []string{
		"main.py",
		"main.sh",
		"main",
		dirname + ".py",
		dirname + ".sh",
		dirname,
	}

	subEntries, err := BuiltinExtensionsFS.ReadDir(fsDir)
	if err != nil {
		return "", err
	}

	names := make(map[string]bool, len(subEntries))
	for _, se := range subEntries {
		if !se.IsDir() {
			names[se.Name()] = true
		}
	}

	for _, c := range candidates {
		if names[c] {
			return c, nil
		}
	}

	// Fallback: first file alphabetically.
	for _, se := range subEntries {
		if !se.IsDir() {
			return se.Name(), nil
		}
	}

	return "", fmt.Errorf("no entry point found in builtin extension subdirectory %q", fsDir)
}
