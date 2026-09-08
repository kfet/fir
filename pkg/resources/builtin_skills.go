package resources

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kfet/fir/pkg/cache"
	"github.com/kfet/fir/pkg/envvars"
)

//go:embed builtin_skills
var BuiltinSkillsFS embed.FS

var (
	builtinExtractOnce sync.Once
	builtinExtractDir  string
	builtinExtractErr  error
)

// builtinSkillsCacheDir locates the parent directory holding extracted
// builtin-skill trees. Tests override it to avoid touching the real cache.
var builtinSkillsCacheDir = defaultBuiltinSkillsCacheDir

func defaultBuiltinSkillsCacheDir() (string, error) { return cache.Dir("builtin-skills") }

// builtinSkillsHash is a deterministic hash of the extracted tree's
// contents — embedded file paths and their *post-expansion* bytes, so a
// change to the env-vars table that expandSkillPlaceholders injects
// produces a different directory rather than a stale one.
func builtinSkillsHash() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(BuiltinSkillsFS, "builtin_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		if d.IsDir() {
			return nil
		}
		data, err := BuiltinSkillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		if d.Name() == "SKILL.md" {
			data = expandSkillPlaceholders(data)
		}
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// extractBuiltinSkills extracts the entire builtin_skills/ tree so that
// BaseDir/scripts paths work at runtime, and returns the directory.
// Called once per process via sync.Once.
//
// The destination is content-addressed — <cache>/fir/builtin-skills/<hash>/
// — not a fresh os.MkdirTemp. A per-process temp dir leaked one copy of
// the whole tree per fir invocation (hundreds of megabytes over a week
// on a busy host, with nothing ever collecting them) and made the skill
// paths this package hands the model unstable across processes for no
// benefit: the content is identical whenever the binary is. Extraction
// is atomic (write to a sibling temp dir, rename into place), so
// concurrent fir processes never observe a partial tree.
func extractBuiltinSkills() (string, error) {
	builtinExtractOnce.Do(func() {
		builtinExtractDir, builtinExtractErr = extractBuiltinSkillsTo()
	})
	return builtinExtractDir, builtinExtractErr
}

func extractBuiltinSkillsTo() (string, error) {
	base, err := builtinSkillsCacheDir()
	if err != nil {
		return "", fmt.Errorf("extract builtin skills: %w", err)
	}
	hash, err := builtinSkillsHash()
	if err != nil {
		return "", fmt.Errorf("hash builtin skills: %w", err)
	}
	dir := filepath.Join(base, hash)

	// Already extracted: claim it (mtime = last use, which is what the
	// collector below ages out on) and use it as-is.
	if _, statErr := os.Stat(dir); statErr == nil {
		cache.Claim(dir)
		go sweepStaleBuiltinSkills(base, dir)
		return dir, nil
	}

	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("extract builtin skills: mkdir cache: %w", err)
	}
	tmp, err := os.MkdirTemp(base, ".extract-")
	if err != nil {
		return "", fmt.Errorf("create temp dir for builtin skills: %w", err)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tmp)
		}
	}()

	err = fs.WalkDir(BuiltinSkillsFS, "builtin_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip "builtin_skills/" prefix to get relative path
		rel := strings.TrimPrefix(path, "builtin_skills/")
		if rel == "" || path == "builtin_skills" {
			return nil
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := BuiltinSkillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if strings.HasSuffix(path, ".sh") {
			perm = 0o755
		}
		// Expand template placeholders in SKILL.md files.
		if d.Name() == "SKILL.md" {
			data = expandSkillPlaceholders(data)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, perm)
	})
	if err != nil {
		return "", fmt.Errorf("extract builtin skills: %w", err)
	}

	if err := os.Rename(tmp, dir); err != nil {
		// Another process won the race — its tree has identical content.
		if _, statErr := os.Stat(dir); statErr == nil {
			success = true // already renamed away or superseded; nothing to clean
			os.RemoveAll(tmp)
			go sweepStaleBuiltinSkills(base, dir)
			return dir, nil
		}
		return "", fmt.Errorf("extract builtin skills: rename: %w", err)
	}
	success = true
	go sweepStaleBuiltinSkills(base, dir)
	return dir, nil
}

// sweepStaleBuiltinSkills collects extracted trees nobody has claimed in
// a while: older hashes under the cache dir, plus the legacy per-process
// $TMPDIR extractions that earlier fir versions left behind. Both are
// aged out rather than deleted eagerly, because a long-running session
// started by an older binary still holds absolute paths into its tree.
// Best-effort throughout: a failure here is not worth failing a session.
func sweepStaleBuiltinSkills(base, keep string) {
	cache.SweepAged(base, keep, cache.MaxAge, cache.NotDotted)
	cache.SweepLegacy("builtin-skills")
	// The pre-cache extractions were per-process dirs sitting directly in
	// $TMPDIR, not trees under one base, so they are matched by name.
	cache.SweepAged(os.TempDir(), "", cache.LegacyTmpMaxAge, func(name string) bool {
		return strings.HasPrefix(name, "fir-builtin-skills-")
	})
}

// BuiltinSkillsDir returns the directory where builtin skills are extracted.
// Returns empty string if extraction hasn't happened or failed.
func BuiltinSkillsDir() string {
	dir, _ := extractBuiltinSkills()
	return dir
}

// LoadBuiltinSkills loads skills from the embedded builtin_skills/ filesystem.
func LoadBuiltinSkills() LoadSkillsResult {
	var skills []Skill
	var diagnostics []ResourceDiagnostic

	extractDir, err := extractBuiltinSkills()
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{
			Type:    "warning",
			Message: err.Error(),
		})
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	_ = fs.WalkDir(BuiltinSkillsFS, "builtin_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}

		data, readErr := BuiltinSkillsFS.ReadFile(path)
		if readErr != nil {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "warning",
				Message: readErr.Error(),
				Path:    path,
			})
			return nil
		}

		fm := parseFrontmatterSimple(string(data))
		if fm.Description == "" || !fm.Builtin {
			return nil
		}

		// Derive name from parent directory
		rel := strings.TrimPrefix(path, "builtin_skills/")
		skillDir := filepath.Dir(rel)
		name := fm.Name
		if name == "" {
			name = filepath.Base(skillDir)
		}

		// Point FilePath and BaseDir at the extracted temp directory
		skills = append(skills, Skill{
			Name:        name,
			Description: fm.Description,
			FilePath:    filepath.Join(extractDir, rel),
			BaseDir:     filepath.Join(extractDir, skillDir),
			Source:      "builtin",
			Origin:      "builtin",
			ID:          MakeSkillID("builtin", name),
			// Builtin skills carry `override: true` in their frontmatter so
			// that when the same file is also discovered via a project skills
			// directory (e.g. `.fir/skills` symlinked at the builtin source
			// tree, or a user-copied skill), the project-origin copy wins.
			// The builtin-origin self-load must not claim override itself,
			// otherwise both copies would claim it and trigger an
			// override-conflict diagnostic.
			Override: "",
		})
		return nil
	})

	return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
}

// expandSkillPlaceholders replaces known template markers in builtin SKILL.md
// files so that documentation stays in sync with the code.
//
// Supported placeholders:
//
//	{{FIR_ENV_VARS_TABLE}} — Markdown table of all public environment variables.
func expandSkillPlaceholders(data []byte) []byte {
	s := string(data)
	if strings.Contains(s, "{{FIR_ENV_VARS_TABLE}}") {
		s = strings.ReplaceAll(s, "{{FIR_ENV_VARS_TABLE}}", envvars.FormatMarkdownTable())
	}
	return []byte(s)
}
