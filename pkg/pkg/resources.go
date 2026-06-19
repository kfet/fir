package pkg

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FirManifest is the schema for fir.json at the package root.
type FirManifest struct {
	Extensions []string `json:"extensions"`
	Skills     []string `json:"skills"`
	Themes     []string `json:"themes"`
}

// PackageResources holds the absolute paths of resources discovered in a package.
type PackageResources struct {
	Extensions []string
	Skills     []string
	Themes     []string
}

// ScanPackageResources reads or auto-discovers resources in dir.
// If fir.json is present its glob patterns are expanded; otherwise
// auto-discovery rules apply.
func ScanPackageResources(dir string) (*PackageResources, error) {
	manifestPath := filepath.Join(dir, "fir.json")
	data, err := os.ReadFile(manifestPath)
	if err == nil {
		// Manifest found — use it.
		var m FirManifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parsing fir.json in %s: %w", dir, err)
		}
		return expandManifest(dir, &m)
	}

	// No manifest — auto-discover.
	return autoDiscover(dir)
}

// expandManifest expands glob patterns from a manifest relative to dir.
func expandManifest(dir string, m *FirManifest) (*PackageResources, error) {
	res := &PackageResources{}
	var err error
	res.Extensions, err = globPatterns(dir, m.Extensions)
	if err != nil {
		return nil, err
	}
	res.Skills, err = globPatterns(dir, m.Skills)
	if err != nil {
		return nil, err
	}
	res.Themes, err = globPatterns(dir, m.Themes)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// globPatterns expands each pattern against dir, returning absolute paths
// of matching files (not directories). A trailing "/" treats the pattern
// as a directory to walk for all files inside.
func globPatterns(dir string, patterns []string) ([]string, error) {
	var result []string
	for _, pat := range patterns {
		// Pattern ending in "/" means "all files under this subdir"
		if strings.HasSuffix(pat, "/") {
			sub := filepath.Join(dir, strings.TrimSuffix(pat, "/"))
			// The closure swallows every walk error (an unreadable or missing
			// entry is skipped, not fatal) and WalkDir only ever returns what
			// the closure returns, so the walk itself cannot fail here.
			_ = filepath.WalkDir(sub, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // skip unreadable entries
				}
				if !d.IsDir() {
					result = append(result, p)
				}
				return nil
			})
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				result = append(result, m)
			}
		}
	}
	return result, nil
}

// skipDirs are directory names ignored during auto-discovery walks.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

// nonSkillMD lists root-level .md filenames that are never skills.
var nonSkillMD = map[string]bool{
	"README.md":          true,
	"readme.md":          true,
	"CHANGELOG.md":       true,
	"changelog.md":       true,
	"CONTRIBUTING.md":    true,
	"contributing.md":    true,
	"LICENSE.md":         true,
	"license.md":         true,
	"AGENTS.md":          true,
	"agents.md":          true,
	"CLAUDE.md":          true,
	"claude.md":          true,
	"CODE_OF_CONDUCT.md": true,
}

// autoDiscover applies the default discovery rules when no fir.json is present.
func autoDiscover(dir string) (*PackageResources, error) {
	res := &PackageResources{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			// Directory-as-extension: a dir containing an extensionless
			// executable entry point (`main` or `<dirname>`, frontmatter
			// optional) is an extension named after the directory — the same
			// convention as .fir/extensions/<name>/. This lets packages ship
			// binary or runtime-wrapped (main → run.sh) extensions. Naming and
			// the frontmatter rules are applied downstream in
			// extension.ConfigsFromFiles.
			if entry := dirEntryPoint(p); entry != "" {
				res.Extensions = append(res.Extensions, entry)
			}
			return nil
		}

		// A file is "at root" when its parent is the package directory
		// itself — or when it *is* the scanned path, which happens when a
		// single file is installed as a package. WalkDir yields paths under
		// dir, so comparing cleaned paths is exact and cannot fail.
		cleanDir := filepath.Clean(dir)
		atRoot := filepath.Dir(p) == cleanDir || filepath.Clean(p) == cleanDir
		name := d.Name()
		ext := strings.ToLower(filepath.Ext(name))
		parentDir := filepath.Base(filepath.Dir(p))

		// Extensions: *.py and *.sh at any depth.
		if ext == ".py" || ext == ".sh" {
			res.Extensions = append(res.Extensions, p)
			return nil
		}

		// Skills:
		//   - SKILL.md anywhere in a subdir
		//   - *.md at root level only, excluding well-known documentation files
		if name == "SKILL.md" && !atRoot {
			res.Skills = append(res.Skills, p)
			return nil
		}
		if ext == ".md" && atRoot && !nonSkillMD[name] {
			res.Skills = append(res.Skills, p)
			return nil
		}

		// Themes: files named theme.json or *.json inside a "themes" subdir.
		if ext == ".json" {
			if name == "theme.json" || parentDir == "themes" {
				res.Themes = append(res.Themes, p)
				return nil
			}
		}

		return nil
	})

	return res, err
}

// dirEntryPoint returns the path to an extensionless executable entry point
// (`main`, or a file named after the directory) directly inside dir, or "" if
// none exists. It mirrors the entry-point convention used for
// .fir/extensions/<name>/ subdirectories, so a package can ship a binary or a
// `main → run.sh` runtime-wrapper extension that carries no comment
// frontmatter. Scripts (.py/.sh) are intentionally excluded here — they are
// collected as individual files and must declare frontmatter.
//
// os.Stat follows symlinks, so a `main → run.sh` symlink whose target is an
// executable regular file qualifies. A dangling `main → …/run.sh` symlink
// (whose SDK-cache target was pruned) is self-healed to the current SDK's
// run.sh so the extension keeps loading across SDK upgrades.
func dirEntryPoint(dir string) string {
	for _, cand := range []string{"main", filepath.Base(dir)} {
		if filepath.Ext(cand) != "" {
			continue // only extensionless entry points
		}
		p := filepath.Join(dir, cand)
		info, err := os.Stat(p)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return p
		}
		// Self-heal a dangling runtime-wrapper symlink, then re-check.
		if healRunShSymlink(p) {
			return p
		}
	}
	return ""
}

// healRunShSymlink repairs a dangling symlink at p that points at a `run.sh`
// in a now-missing SDK cache directory, re-pointing it to the current SDK's
// run.sh. Returns true only when p ends up a valid `run.sh` symlink. It is a
// no-op (false) for non-symlinks, already-valid symlinks, and symlinks that do
// not target a `run.sh` (so it never touches unrelated user symlinks).
func healRunShSymlink(p string) bool {
	target, err := os.Readlink(p)
	if err != nil {
		return false // not a symlink
	}
	if filepath.Base(target) != "run.sh" {
		return false // not one of our runtime-wrapper symlinks
	}
	if _, err := os.Stat(p); err == nil {
		return true // already resolves — nothing to heal
	}
	runSh, err := jsRunShPath()
	if err != nil || runSh == "" {
		return false
	}
	if err := os.Remove(p); err != nil {
		return false
	}
	if err := os.Symlink(runSh, p); err != nil {
		return false
	}
	return true
}
