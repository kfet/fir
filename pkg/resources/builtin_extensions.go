package resources

import (
	"crypto/sha256"
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

	builtinExtHashOnce sync.Once
	builtinExtHash     string
)

// BuiltinExtensionsHash returns a deterministic 16-hex-char SHA-256 prefix of
// every embedded builtin extension file (path + contents). It changes whenever
// any builtin extension is added, removed, or modified — which makes it useful
// for detecting that a /reexec has crossed a release boundary that changed the
// set of builtin extensions.
//
// The value is computed once and cached.
func BuiltinExtensionsHash() string {
	builtinExtHashOnce.Do(func() {
		h := sha256.New()
		_ = fs.WalkDir(BuiltinExtensionsFS, "builtin_extensions", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := BuiltinExtensionsFS.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write([]byte(path))
			h.Write(data)
			return nil
		})
		builtinExtHash = fmt.Sprintf("%x", h.Sum(nil))[:16]
	})
	return builtinExtHash
}

// ExtensionFrontmatter holds metadata parsed from a comment frontmatter block
// at the top of an extension script.
type ExtensionFrontmatter struct {
	Name        string
	Description string
	Builtin     bool
	// Explicit marks an extension as opt-in: it is discovered (so `-e <name>`
	// can find it) but is NOT auto-loaded. Use for example/demo extensions
	// that should only run when the user asks for them by name.
	Explicit      bool
	Modes         []string
	AuthProviders []string // auth provider IDs this extension registers
	CLIVerbs      []string // top-level `fir <verb>` names this extension claims
	Present       bool     // true when a valid frontmatter block was found
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
			fm.Present = true
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
		case "explicit":
			fm.Explicit = value == "true"
		case "mode", "modes":
			fm.Modes = parseExtensionModes(value)
		case "auth_provider", "auth_providers":
			fm.AuthProviders = parseCommaSeparatedList(value)
		case "cli_verb", "cli_verbs":
			fm.CLIVerbs = parseCommaSeparatedList(value)
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

// parseCommaSeparatedList parses a bracket-optional, comma-separated list of strings.
// e.g. "agent_end, turn_end" or "[agent_end, turn_end]"
func parseCommaSeparatedList(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		s := strings.Trim(strings.TrimSpace(part), `"'`)
		if s != "" {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// extractBuiltinExtensions extracts the builtin_extensions/ tree to a temp
// directory so extension scripts can be executed as subprocesses.
func extractBuiltinExtensions() (string, error) {
	builtinExtExtractOnce.Do(func() {
		cacheBase := filepath.Join(os.TempDir(), "fir-builtin-extensions")
		builtinExtExtractDir, builtinExtExtractErr = extractBuiltinExtensionsTo(cacheBase)
	})
	return builtinExtExtractDir, builtinExtExtractErr
}

// extractBuiltinExtensionsTo materialises the embedded builtin_extensions tree
// under cacheBase/<hash>/ and returns the resulting directory. It is safe to
// call repeatedly: a sentinel `.complete` file is written last on extraction,
// and its presence on a subsequent call means the cached directory is whole
// (so we can reuse it). If the directory is missing the sentinel — empty,
// partially purged by macOS temp cleanup, or otherwise incomplete — we wipe
// and re-extract.
func extractBuiltinExtensionsTo(cacheBase string) (string, error) {
	hash := BuiltinExtensionsHash()
	dir := filepath.Join(cacheBase, hash)
	sentinel := filepath.Join(dir, ".complete")

	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		if _, sErr := os.Stat(sentinel); sErr == nil {
			return dir, nil
		}
		// Directory exists but is incomplete — remove and re-extract.
		os.RemoveAll(dir)
	}

	if err := os.MkdirAll(cacheBase, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir for builtin extensions: %w", err)
	}

	tmp, err := os.MkdirTemp(cacheBase, ".extract-")
	if err != nil {
		return "", fmt.Errorf("create temp dir for builtin extensions: %w", err)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tmp)
		}
	}()

	err = fs.WalkDir(BuiltinExtensionsFS, "builtin_extensions", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(path, "builtin_extensions/")
		if rel == "" || path == "builtin_extensions" {
			return nil
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := BuiltinExtensionsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o755)
	})
	if err != nil {
		return "", fmt.Errorf("extract builtin extensions: %w", err)
	}

	// Write sentinel marker last, so its presence guarantees a complete extraction.
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", fmt.Errorf("write builtin extensions sentinel: %w", err)
	}

	if err := os.Rename(tmp, dir); err != nil {
		// Rename may fail if another process created it concurrently.
		if _, statErr := os.Stat(dir); statErr == nil {
			os.RemoveAll(tmp)
			return dir, nil
		}
		return "", fmt.Errorf("rename builtin extensions dir: %w", err)
	}
	success = true
	return dir, nil
}

// BuiltinExtension describes a builtin extension discovered from the embedded FS.
type BuiltinExtension struct {
	Name string
	Path string // absolute path to the extracted executable
}

// LoadBuiltinExtensions returns extensions marked with builtin: true OR
// explicit: true in their comment frontmatter. Builtin extensions are
// auto-loaded by default; explicit extensions are discovered so `-e <name>`
// can find them but are skipped from default loading by the manager.
// The scripts are extracted to a temp directory so they can be executed as
// subprocesses.
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
		if !fm.Builtin && !fm.Explicit {
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
	if !fm.Builtin && !fm.Explicit {
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
