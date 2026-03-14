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
	Prompts    []string `json:"prompts"`
	Themes     []string `json:"themes"`
}

// PackageResources holds the absolute paths of resources discovered in a package.
type PackageResources struct {
	Extensions []string
	Skills     []string
	Prompts    []string
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
	res.Prompts, err = globPatterns(dir, m.Prompts)
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
			err := filepath.WalkDir(sub, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // skip unreadable entries
				}
				if !d.IsDir() {
					result = append(result, p)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
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
			return nil
		}

		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return nil
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		atRoot := len(parts) == 1
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
