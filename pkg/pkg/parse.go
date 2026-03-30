package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source represents a parsed package source string.
type Source struct {
	Type   string // "git" | "local"
	Raw    string // original user input
	Host   string // e.g. "github.com"
	Path   string // e.g. "user/repo"
	SubDir string // subdirectory within repo (e.g. "external_plugins/telegram")
	URL    string // canonical HTTPS clone URL (empty for local)
	Ref    string // optional branch/tag/commit
	Pinned bool   // true if ref was explicitly specified
	Local  string // for Type=="local": resolved absolute path
}

// ParseSource parses a source string into a Source struct.
//
// Supported formats:
//   - Local path:  ./rel, /abs, ~/home, C:\windows
//   - SSH URL:     git@host:path[@ref]
//   - HTTPS URL:   https://host/path[@ref]
//   - Bare:        host/path[@ref]  (host must contain a ".")
func ParseSource(s string) (*Source, error) {
	if s == "" {
		return nil, fmt.Errorf("empty source")
	}

	src := &Source{Raw: s}

	// --- Local path detection ---
	if isLocalPath(s) {
		resolved, err := resolveLocalPath(s)
		if err != nil {
			return nil, fmt.Errorf("resolving local path %q: %w", s, err)
		}
		src.Type = "local"
		src.Local = resolved
		return src, nil
	}

	// --- SSH URL: git@host:path[@ref] ---
	if strings.HasPrefix(s, "git@") {
		return parseSSH(src, s)
	}

	// --- HTTPS URL: https://host/path[@ref] ---
	if strings.HasPrefix(s, "https://") {
		return parseHTTPS(src, s)
	}

	// --- Bare: host/path[@ref] ---
	// Only treat as git if the first component looks like a domain (contains ".").
	if idx := strings.Index(s, "/"); idx > 0 {
		host := s[:idx]
		if strings.Contains(host, ".") {
			return parseBare(src, s)
		}
	}

	// Fallback: treat as local path.
	resolved, err := resolveLocalPath(s)
	if err != nil {
		return nil, fmt.Errorf("resolving local path %q: %w", s, err)
	}
	src.Type = "local"
	src.Local = resolved
	return src, nil
}

// isLocalPath reports whether s looks like a filesystem path.
func isLocalPath(s string) bool {
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return true
	}
	return isWindowsAbsolute(s)
}

// isWindowsAbsolute reports whether s looks like a Windows absolute path.
func isWindowsAbsolute(s string) bool {
	if len(s) >= 3 {
		c := s[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			if s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
				return true
			}
		}
	}
	return false
}

// resolveLocalPath expands "~" and makes the path absolute.
func resolveLocalPath(s string) (string, error) {
	if strings.HasPrefix(s, "~/") || s == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		s = filepath.Join(home, s[1:])
	}
	return filepath.Abs(s)
}

// parseSSH handles git@host:path[@ref].
func parseSSH(src *Source, s string) (*Source, error) {
	// Strip "git@"
	rest := s[len("git@"):]
	// Split on ":"
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return nil, fmt.Errorf("invalid SSH source %q: missing ':'", s)
	}
	src.Host = rest[:colonIdx]
	pathAndRef := rest[colonIdx+1:]

	repoPath, ref := splitRef(pathAndRef)
	// Normalise: strip trailing ".git"
	repoPath = strings.TrimSuffix(repoPath, ".git")

	repo, subDir := splitRepoSubDir(src.Host, repoPath)

	src.Type = "git"
	src.Path = repo
	src.SubDir = subDir
	src.Ref = ref
	src.Pinned = ref != ""
	src.URL = "https://" + src.Host + "/" + repo
	return src, nil
}

// parseHTTPS handles https://host/path[@ref].
func parseHTTPS(src *Source, s string) (*Source, error) {
	// Strip scheme
	rest := strings.TrimPrefix(s, "https://")
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return nil, fmt.Errorf("invalid HTTPS source %q: missing path", s)
	}
	src.Host = rest[:slashIdx]
	pathAndRef := rest[slashIdx+1:]

	repoPath, ref := splitRef(pathAndRef)
	repoPath = strings.TrimSuffix(repoPath, ".git")

	repo, subDir := splitRepoSubDir(src.Host, repoPath)

	src.Type = "git"
	src.Path = repo
	src.SubDir = subDir
	src.Ref = ref
	src.Pinned = ref != ""
	src.URL = "https://" + src.Host + "/" + repo
	return src, nil
}

// parseBare handles host/path[@ref] (no scheme).
func parseBare(src *Source, s string) (*Source, error) {
	slashIdx := strings.Index(s, "/")
	src.Host = s[:slashIdx]
	pathAndRef := s[slashIdx+1:]

	repoPath, ref := splitRef(pathAndRef)
	repoPath = strings.TrimSuffix(repoPath, ".git")

	repo, subDir := splitRepoSubDir(src.Host, repoPath)

	src.Type = "git"
	src.Path = repo
	src.SubDir = subDir
	src.Ref = ref
	src.Pinned = ref != ""
	src.URL = "https://" + src.Host + "/" + repo
	return src, nil
}

// splitRef splits "path/to/repo@ref" into ("path/to/repo", "ref").
// If no "@" is present, ref is "".
func splitRef(s string) (path, ref string) {
	atIdx := strings.LastIndex(s, "@")
	if atIdx < 0 {
		return s, ""
	}
	return s[:atIdx], s[atIdx+1:]
}

// knownTwoComponentHosts lists git hosting services where the repo path is
// always exactly two components (owner/repo). Extra path components are
// treated as a subdirectory within the repo.
var knownTwoComponentHosts = map[string]bool{
	"github.com":    true,
	"bitbucket.org": true,
}

// splitRepoSubDir splits a full path like "org/repo/sub/dir" into the repo
// path ("org/repo") and subdirectory ("sub/dir") based on the host.
// For known two-component hosts, the first two segments are the repo.
// For other hosts, the entire path is treated as the repo (no subdir).
func splitRepoSubDir(host, fullPath string) (repoPath, subDir string) {
	if !knownTwoComponentHosts[host] {
		return fullPath, ""
	}
	parts := strings.SplitN(fullPath, "/", 3)
	if len(parts) <= 2 {
		return fullPath, ""
	}
	return parts[0] + "/" + parts[1], parts[2]
}
