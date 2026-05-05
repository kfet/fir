// Ported from: packages/coding-agent/src/core/skills.ts
// Upstream hash: 1caadb2e
package resources

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxSkillNameLength per Agent Skills spec.
	MaxSkillNameLength = 64
	// MaxSkillDescriptionLength per Agent Skills spec.
	MaxSkillDescriptionLength = 1024
)

// Skill represents a loaded skill.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
	BaseDir     string `json:"baseDir"`
	Source      string `json:"source"` // "user", "project", or "path"
}

// ResourceCollision describes a name collision between resources.
type ResourceCollision struct {
	ResourceType string `json:"resourceType"` // "extension", "skill", "prompt", "theme"
	Name         string `json:"name"`
	WinnerPath   string `json:"winnerPath"`
	LoserPath    string `json:"loserPath"`
}

// ResourceDiagnostic reports a warning or error during resource loading.
type ResourceDiagnostic struct {
	Type      string             `json:"type"` // "warning", "error", or "collision"
	Message   string             `json:"message"`
	Path      string             `json:"path,omitempty"`
	Collision *ResourceCollision `json:"collision,omitempty"`
}

// LoadSkillsResult contains loaded skills and diagnostics.
type LoadSkillsResult struct {
	Skills      []Skill              `json:"skills"`
	Diagnostics []ResourceDiagnostic `json:"diagnostics"`
}

// SkillFrontmatter is the YAML frontmatter in a skill file.
type SkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Builtin     bool   `yaml:"builtin"`
}

// LoadSkillsFromDir loads skills from a directory.
// Discovery rules:
// - Direct .md children in the root
// - Recursive SKILL.md under subdirectories
func LoadSkillsFromDir(dir, source string) LoadSkillsResult {
	return loadSkillsFromDirInternal(dir, source, true, map[string]bool{})
}

func loadSkillsFromDirInternal(dir, source string, includeRootFiles bool, visited map[string]bool) LoadSkillsResult {
	var skills []Skill
	var diagnostics []ResourceDiagnostic

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	// Cycle protection: track resolved directory paths to avoid infinite
	// recursion on symlink cycles.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		realDir = dir
	}
	if visited[realDir] {
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}
	visited[realDir] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}

		fullPath := filepath.Join(dir, name)

		// Resolve symlinks via os.Stat (entry.Type() comes from lstat).
		mode := entry.Type()
		isDir := entry.IsDir()
		isRegular := mode.IsRegular()
		if mode&fs.ModeSymlink != 0 {
			target, err := os.Stat(fullPath)
			if err != nil {
				continue
			}
			isDir = target.IsDir()
			isRegular = target.Mode().IsRegular()
		}

		if isDir {
			sub := loadSkillsFromDirInternal(fullPath, source, false, visited)
			skills = append(skills, sub.Skills...)
			diagnostics = append(diagnostics, sub.Diagnostics...)
			continue
		}

		if !isRegular {
			continue
		}

		isRootMd := includeRootFiles && strings.HasSuffix(name, ".md")
		isSkillMd := !includeRootFiles && name == "SKILL.md"
		if !isRootMd && !isSkillMd {
			continue
		}

		result := loadSkillFromFile(fullPath, source)
		if result.Skill != nil {
			skills = append(skills, *result.Skill)
		}
		diagnostics = append(diagnostics, result.Diagnostics...)
	}

	return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
}

type skillLoadResult struct {
	Skill       *Skill
	Diagnostics []ResourceDiagnostic
}

func loadSkillFromFile(filePath, source string) skillLoadResult {
	var diagnostics []ResourceDiagnostic

	data, err := os.ReadFile(filePath)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{
			Type:    "warning",
			Message: err.Error(),
			Path:    filePath,
		})
		return skillLoadResult{Diagnostics: diagnostics}
	}

	fm := parseFrontmatterSimple(string(data))
	skillDir := filepath.Dir(filePath)
	parentDirName := filepath.Base(skillDir)

	// Validate description
	if fm.Description == "" {
		diagnostics = append(diagnostics, ResourceDiagnostic{
			Type:    "warning",
			Message: "description is required",
			Path:    filePath,
		})
		return skillLoadResult{Diagnostics: diagnostics}
	}

	// Use name from frontmatter, or fall back to filename (sans ext), then parent directory name
	name := fm.Name
	if name == "" {
		base := filepath.Base(filePath)
		ext := filepath.Ext(base)
		fileBaseName := strings.TrimSuffix(base, ext)
		if fileBaseName != "" && fileBaseName != "SKILL" {
			name = fileBaseName
		} else {
			name = parentDirName
		}
	}

	// Validate name
	nameErrors := validateSkillName(name, parentDirName)
	for _, e := range nameErrors {
		diagnostics = append(diagnostics, ResourceDiagnostic{
			Type:    "warning",
			Message: e,
			Path:    filePath,
		})
	}

	return skillLoadResult{
		Skill: &Skill{
			Name:        name,
			Description: fm.Description,
			FilePath:    filePath,
			BaseDir:     skillDir,
			Source:      source,
		},
		Diagnostics: diagnostics,
	}
}

var validSkillNameRegex = regexp.MustCompile(`^[a-z0-9-]+$`)

func validateSkillName(name, parentDirName string) []string {
	var errors []string

	if name != parentDirName {
		errors = append(errors, `name "`+name+`" does not match parent directory "`+parentDirName+`"`)
	}
	if len(name) > MaxSkillNameLength {
		errors = append(errors, "name exceeds max length")
	}
	if !validSkillNameRegex.MatchString(name) {
		errors = append(errors, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errors = append(errors, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errors = append(errors, "name must not contain consecutive hyphens")
	}

	return errors
}

// parseFrontmatterSimple is a basic YAML frontmatter parser.
// Parses --- delimited frontmatter with key: value pairs.
func parseFrontmatterSimple(content string) SkillFrontmatter {
	var fm SkillFrontmatter

	if !strings.HasPrefix(content, "---") {
		return fm
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx == -1 {
		return fm
	}

	fmContent := content[3 : 3+endIdx]
	for _, line := range strings.Split(fmContent, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "name":
			fm.Name = value
		case "description":
			fm.Description = value
		case "builtin":
			fm.Builtin = value == "true"
		}
	}

	return fm
}

// FormatSkillsForPrompt formats skills for inclusion in a system prompt.
func FormatSkillsForPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
	sb.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	sb.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n")
	sb.WriteString("\n<available_skills>\n")

	for _, skill := range skills {
		sb.WriteString("  <skill>\n")
		sb.WriteString("    <name>" + escapeXml(skill.Name) + "</name>\n")
		sb.WriteString("    <description>" + escapeXml(skill.Description) + "</description>\n")
		sb.WriteString("    <location>" + escapeXml(skill.FilePath) + "</location>\n")
		sb.WriteString("  </skill>\n")
	}

	sb.WriteString("</available_skills>")
	return sb.String()
}

func escapeXml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// LoadSkillsOptions configures skill loading.
type LoadSkillsOptions struct {
	Cwd             string
	AgentDir        string
	SkillPaths      []string
	IncludeDefaults bool
}

// LoadSkills loads skills from all configured locations.
func LoadSkills(opts LoadSkillsOptions) LoadSkillsResult {
	if opts.Cwd == "" {
		opts.Cwd, _ = os.Getwd()
	}

	skillMap := make(map[string]Skill)
	var allDiagnostics []ResourceDiagnostic

	realPathSet := make(map[string]bool)

	addSkills := func(result LoadSkillsResult) {
		allDiagnostics = append(allDiagnostics, result.Diagnostics...)
		for _, skill := range result.Skills {
			// Resolve symlinks to detect duplicate files
			realPath := skill.FilePath
			if resolved, err := filepath.EvalSymlinks(skill.FilePath); err == nil {
				realPath = resolved
			}

			// Skip silently if we've already loaded this exact file (via symlink)
			if realPathSet[realPath] {
				continue
			}

			if existing, ok := skillMap[skill.Name]; ok {
				allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
					Type:    "collision",
					Message: fmt.Sprintf("name %q collision", skill.Name),
					Path:    skill.FilePath,
					Collision: &ResourceCollision{
						ResourceType: "skill",
						Name:         skill.Name,
						WinnerPath:   existing.FilePath,
						LoserPath:    skill.FilePath,
					},
				})
			} else {
				skillMap[skill.Name] = skill
				realPathSet[realPath] = true
			}
		}
	}

	if opts.IncludeDefaults && opts.AgentDir != "" {
		addSkills(loadSkillsFromDirInternal(filepath.Join(opts.AgentDir, "skills"), "user", true, map[string]bool{}))
		addSkills(loadSkillsFromDirInternal(filepath.Join(opts.Cwd, ".fir", "skills"), "project", true, map[string]bool{}))
		addSkills(LoadBuiltinSkills())
	}

	for _, rawPath := range opts.SkillPaths {
		resolvedPath := rawPath
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(opts.Cwd, resolvedPath)
		}

		info, err := os.Stat(resolvedPath)
		if err != nil {
			allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
				Type:    "warning",
				Message: "skill path does not exist",
				Path:    resolvedPath,
			})
			continue
		}

		if info.IsDir() {
			addSkills(loadSkillsFromDirInternal(resolvedPath, "path", true, map[string]bool{}))
		} else if strings.HasSuffix(resolvedPath, ".md") {
			result := loadSkillFromFile(resolvedPath, "path")
			if result.Skill != nil {
				addSkills(LoadSkillsResult{
					Skills:      []Skill{*result.Skill},
					Diagnostics: result.Diagnostics,
				})
			} else {
				allDiagnostics = append(allDiagnostics, result.Diagnostics...)
			}
		}
	}

	var skills []Skill
	for _, s := range skillMap {
		skills = append(skills, s)
	}
	// Sort by name for deterministic ordering. Map iteration is randomised,
	// so without this the system prompt's <available_skills> block reorders
	// every process start / Reload, busting the prompt cache.
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	return LoadSkillsResult{Skills: skills, Diagnostics: allDiagnostics}
}
