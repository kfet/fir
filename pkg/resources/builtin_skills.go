package resources

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kfet/fir/pkg/envvars"
)

//go:embed builtin_skills
var BuiltinSkillsFS embed.FS

var (
	builtinExtractOnce sync.Once
	builtinExtractDir  string
	builtinExtractErr  error
)

// extractBuiltinSkills extracts the entire builtin_skills/ tree to a temp
// directory so that BaseDir/scripts paths work at runtime. Called once per
// process via sync.Once.
func extractBuiltinSkills() (string, error) {
	builtinExtractOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fir-builtin-skills-")
		if err != nil {
			builtinExtractErr = fmt.Errorf("create temp dir for builtin skills: %w", err)
			return
		}
		builtinExtractDir = dir

		err = fs.WalkDir(BuiltinSkillsFS, "builtin_skills", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Strip "builtin_skills/" prefix to get relative path
			rel := strings.TrimPrefix(path, "builtin_skills/")
			if rel == "" || path == "builtin_skills" {
				return nil
			}
			target := filepath.Join(dir, rel)
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
			return os.WriteFile(target, data, perm)
		})
		if err != nil {
			builtinExtractErr = fmt.Errorf("extract builtin skills: %w", err)
		}
	})
	return builtinExtractDir, builtinExtractErr
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
