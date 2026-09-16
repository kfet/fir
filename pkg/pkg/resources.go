package pkg

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/extension/sdk"
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
// executable regular file qualifies. A `main → …/run.sh` symlink left behind
// by an older SDK — dangling (cache dir pruned) or merely stale (cache dir
// survives, so it silently keeps running the previous SDK) — is re-pointed at
// the current SDK's run.sh, so the extension both keeps loading and stays in
// step with the SDK across fir upgrades.
func dirEntryPoint(dir string) string {
	for _, cand := range []string{"main", filepath.Base(dir)} {
		if filepath.Ext(cand) != "" {
			continue // only extensionless entry points
		}
		p := filepath.Join(dir, cand)
		// Re-point a stale runtime-wrapper symlink first: an entry that
		// still resolves to a PREVIOUS SDK cache dir would otherwise keep
		// running that SDK's run.sh (and its fir_ext.js) forever.
		if healRunShSymlink(p) {
			return p
		}
		info, err := os.Stat(p)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

// healRunShSymlink brings a runtime-wrapper symlink at p up to date with the
// current SDK, covering both ways it can go stale across a fir upgrade:
//
//   - DANGLING — the SDK cache dir it pointed at was pruned, so the extension
//     stops loading entirely.
//   - STALE — the old cache dir survives, so the symlink still resolves and
//     the extension silently keeps running the PREVIOUS SDK's run.sh (and
//     therefore the previous fir_ext.js/pi_compat.js). This is the nastier of
//     the two: everything looks healthy while the extension is answering with
//     an SDK several releases behind.
//
// Returns true only when p ends up a valid `run.sh` symlink. It is a no-op
// (false) for non-symlinks and for symlinks that do not target a `run.sh`
// inside an SDK cache directory, so a hand-made symlink to a user's own
// run.sh is never touched.
func healRunShSymlink(p string) bool {
	target, err := os.Readlink(p)
	if err != nil {
		return false // not a symlink
	}
	if filepath.Base(target) != "run.sh" {
		return false // not one of our runtime-wrapper symlinks
	}
	_, statErr := os.Stat(p)
	resolves := statErr == nil
	// Only ever rewrite a link into fir's SDK cache. A resolving link that
	// points somewhere else is the user's, and is left exactly as-is.
	if resolves && !isSDKCachePath(target) {
		return true
	}
	runSh, err := jsRunShPath()
	if err != nil || runSh == "" {
		return resolves
	}
	if resolves && target == runSh {
		return true // already current — nothing to do
	}
	if err := replaceSymlink(p, runSh); err != nil {
		return resolves
	}
	return true
}

// replaceSymlink points link at target, atomically. A plain remove-then-create
// leaves NO entry at all if the process dies (or loses a race with a
// concurrent session) in between, and nothing recreates it outside
// `fir install` — the extension would go permanently undiscoverable. Creating
// a uniquely-named temporary link and renaming it over the old one is atomic
// on POSIX: readers see either the old link or the new one, never neither.
func replaceSymlink(link, target string) error {
	tmp := fmt.Sprintf("%s.tmp-%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// isSDKCachePath reports whether target lives inside fir's extracted-SDK cache
// directory, which is the only place healRunShSymlink is allowed to re-point a
// symlink away from. The cache root is resolved from the SDK package itself
// rather than matched by directory NAME: a basename test for "sdks" would also
// claim a user's own `~/projects/sdks/myfork/run.sh` and silently overwrite a
// hand-made symlink fir has no business touching.
func isSDKCachePath(target string) bool {
	root, err := sdkCacheRoot()
	if err != nil || root == "" {
		return false
	}
	return strings.HasPrefix(filepath.Clean(target), root+string(os.PathSeparator))
}

// sdkCacheRoot returns the directory holding the per-version extracted SDK
// trees (the parent of the `<hash>` dir that jsRunShPath resolves inside).
func sdkCacheRoot() (string, error) {
	base, err := sdk.EnsureExtracted()
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Clean(base)), nil
}
