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
//
// Same-named skills from different origins are allowed to coexist; the
// agent-facing reference is the bare Name when unique, or the disambiguated
// ID (`<sanitized-origin>__<name>`, MCP-style) when not.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
	BaseDir     string `json:"baseDir"`

	// Source is a short human-readable scope label: "builtin", "user",
	// "project", "package", or "path". Stable for UI/back-compat.
	Source string `json:"source"`

	// Origin is the precise provenance label used to derive ID and to
	// disambiguate. Examples: "builtin", "user", "project",
	// "pkg:github.com/foo/bar", "path:basename".
	Origin string `json:"origin"`

	// ID is the unique identifier across all loaded skills, of the form
	// "<sanitized-origin>__<name>".
	ID string `json:"id"`

	// Override declares this skill replaces another with the same name.
	// "" = coexist (default); "true" = replace any other same-named skill;
	// "<full-id>" = replace specifically that target.
	Override string `json:"override,omitempty"`

	// Overridden lists IDs of skills this one displaced (populated by LoadSkills).
	Overridden []string `json:"overridden,omitempty"`
}

// ResourceCollision describes a name collision between resources.
type ResourceCollision struct {
	ResourceType string `json:"resourceType"` // "extension", "skill", "prompt", "theme"
	Name         string `json:"name"`
	WinnerPath   string `json:"winnerPath"`
	LoserPath    string `json:"loserPath"`
}

// ResourceDiagnostic reports a warning or error during resource loading.
//
// Type values:
//   - "warning"        — generic load problem (bad frontmatter, etc.)
//   - "error"          — fatal load problem
//   - "shadowed"       — skill suppressed by an override or by real-path dedup
//   - "duplicate-name" — informational; multiple skills coexist under the same
//     bare name and must be referenced by ID
//   - "override-conflict" — multiple skills claim override of the same name
type ResourceDiagnostic struct {
	Type      string             `json:"type"`
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
	Override    string `yaml:"override"`
}

// SkillRoot describes one skill source-root to walk, with its origin
// pre-determined by the caller (since only the caller knows whether a path
// belongs to a package, the user dir, etc.).
type SkillRoot struct {
	Path   string
	Source string // short label for UI: "builtin"|"user"|"project"|"package"|"path"
	Origin string // precise origin: "builtin"|"user"|"project"|"pkg:<src>"|"path:<label>"
}

// SanitizeOriginForID converts an origin label into a form usable inside an
// MCP-style tool/skill ID (`<origin>__<name>`). Lowercases ASCII letters and
// replaces every other character with `_`.
func SanitizeOriginForID(origin string) string {
	var b strings.Builder
	b.Grow(len(origin))
	for _, r := range origin {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// MakeSkillID returns the canonical disambiguated ID for a skill.
func MakeSkillID(origin, name string) string {
	return SanitizeOriginForID(origin) + "__" + name
}

// LoadSkillsFromDir loads skills from a directory.
// Discovery rules:
// - Direct .md children in the root
// - Recursive SKILL.md under subdirectories
func LoadSkillsFromDir(dir, source string) LoadSkillsResult {
	return LoadSkillsFromRoot(SkillRoot{Path: dir, Source: source, Origin: source})
}

// LoadSkillsFromRoot is like LoadSkillsFromDir but also tags Skill.Origin.
func LoadSkillsFromRoot(root SkillRoot) LoadSkillsResult {
	return loadSkillsFromDirInternal(root.Path, root.Source, root.Origin, true, map[string]bool{})
}

func loadSkillsFromDirInternal(dir, source, origin string, includeRootFiles bool, visited map[string]bool) LoadSkillsResult {
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
			sub := loadSkillsFromDirInternal(fullPath, source, origin, false, visited)
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

		result := loadSkillFromFile(fullPath, source, origin)
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

func loadSkillFromFile(filePath, source, origin string) skillLoadResult {
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

	// Default origin to source if caller did not provide one (e.g. legacy
	// LoadSkillsFromDir).
	if origin == "" {
		origin = source
	}

	return skillLoadResult{
		Skill: &Skill{
			Name:        name,
			Description: fm.Description,
			FilePath:    filePath,
			BaseDir:     skillDir,
			Source:      source,
			Origin:      origin,
			ID:          MakeSkillID(origin, name),
			Override:    fm.Override,
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
		case "override":
			fm.Override = value
		}
	}

	return fm
}

// AgentReference returns the name the agent should use to invoke this skill,
// given the full set of loaded skills. Bare name when unique, ID when not.
func (s Skill) AgentReference(all []Skill) string {
	count := 0
	for _, other := range all {
		if other.Name == s.Name {
			count++
			if count > 1 {
				return s.ID
			}
		}
	}
	return s.Name
}

// FormatSkillsForPrompt formats skills for inclusion in a system prompt.
//
// When two skills share a bare name (after override resolution), each is
// emitted under its disambiguated ID (`<origin>__<name>`); otherwise the bare
// name is used. The agent always reads the file via <location>.
func FormatSkillsForPrompt(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	// Count bare-name occurrences to decide per-skill whether to disambiguate.
	nameCount := make(map[string]int, len(skills))
	for _, s := range skills {
		nameCount[s.Name]++
	}
	hasAmbiguous := false
	for _, c := range nameCount {
		if c > 1 {
			hasAmbiguous = true
			break
		}
	}

	var sb strings.Builder
	sb.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
	sb.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	sb.WriteString("When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n")
	if hasAmbiguous {
		sb.WriteString("Some skills share a base name; for those, the disambiguated form `<origin>__<name>` is shown — reference them by that exact name.\n")
	}
	sb.WriteString("\n<available_skills>\n")

	for _, skill := range skills {
		ref := skill.Name
		if nameCount[skill.Name] > 1 {
			ref = skill.ID
		}
		sb.WriteString("  <skill>\n")
		sb.WriteString("    <name>" + escapeXml(ref) + "</name>\n")
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

// originPrecedence ranks origins for tie-breaking among override claims.
// Higher score wins. user > project > path:* > pkg:* > builtin; any
// completely unknown origin string falls below builtin.
func originPrecedence(origin string) int {
	switch {
	case origin == "user":
		return 5
	case origin == "project":
		return 4
	case strings.HasPrefix(origin, "path:"):
		return 3
	case strings.HasPrefix(origin, "pkg:"):
		return 2
	case origin == "builtin":
		return 1
	default:
		return 0
	}
}

// resolveOverrides applies override semantics to a flat list of skills.
// Returns the surviving skills (in input order, with Overridden populated)
// and any diagnostics (shadowed/override-conflict).
func resolveOverrides(skills []Skill) ([]Skill, []ResourceDiagnostic) {
	var diagnostics []ResourceDiagnostic

	// Index skills by ID for explicit-target overrides.
	byID := make(map[string]int, len(skills))
	for i, s := range skills {
		byID[s.ID] = i
	}

	// Collect override actions: which IDs are slated for removal, and by whom.
	type killEntry struct {
		killerID  string
		killerIdx int
	}
	killed := make(map[string]killEntry) // victim ID -> killer

	// First pass: explicit-ID overrides.
	for i, s := range skills {
		if s.Override == "" || s.Override == "true" {
			continue
		}
		// Allow callers to write the target either as the raw origin form
		// (e.g. "path:foo__bar", "pkg:github.com/x/y__skill") or the
		// already-sanitized ID. Look up by both.
		target := s.Override
		victimIdx, ok := byID[target]
		if !ok {
			// Try sanitizing the origin portion: split on the LAST "__".
			if cut := strings.LastIndex(target, "__"); cut > 0 {
				sanitized := SanitizeOriginForID(target[:cut]) + "__" + target[cut+2:]
				if vi, ok2 := byID[sanitized]; ok2 {
					victimIdx, ok = vi, true
				}
			}
		}
		if !ok {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "warning",
				Path:    s.FilePath,
				Message: fmt.Sprintf("skill %q declares override: %q but no such skill is loaded", s.ID, s.Override),
			})
			continue
		}
		if victimIdx == i {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "warning",
				Path:    s.FilePath,
				Message: fmt.Sprintf("skill %q cannot override itself", s.ID),
			})
			continue
		}
		killed[skills[victimIdx].ID] = killEntry{killerID: s.ID, killerIdx: i}
	}

	// Second pass: `override: true`. For each name, group all skills with
	// override:true; the highest-precedence wins; that winner kills every
	// other same-named skill (including non-overriders); losing overriders
	// produce an override-conflict warning but otherwise coexist.
	byName := make(map[string][]int, len(skills))
	for i, s := range skills {
		byName[s.Name] = append(byName[s.Name], i)
	}
	for name, idxs := range byName {
		if len(idxs) < 2 {
			continue
		}
		// Find override:true claimants (skipping ones already killed).
		var claimants []int
		for _, i := range idxs {
			if _, dead := killed[skills[i].ID]; dead {
				continue
			}
			if skills[i].Override == "true" {
				claimants = append(claimants, i)
			}
		}
		if len(claimants) == 0 {
			continue
		}
		// Pick highest-precedence claimant; tie → first by index for determinism.
		winner := claimants[0]
		for _, c := range claimants[1:] {
			if originPrecedence(skills[c].Origin) > originPrecedence(skills[winner].Origin) {
				winner = c
			}
		}
		if len(claimants) > 1 {
			var others []string
			for _, c := range claimants {
				if c != winner {
					others = append(others, skills[c].ID)
				}
			}
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "override-conflict",
				Path:    skills[winner].FilePath,
				Message: fmt.Sprintf("multiple skills claim override of %q; %q won (others: %s)", name, skills[winner].ID, strings.Join(others, ", ")),
			})
		}
		// Winner kills every other same-named skill that isn't itself.
		for _, i := range idxs {
			if i == winner {
				continue
			}
			if _, already := killed[skills[i].ID]; already {
				continue
			}
			killed[skills[i].ID] = killEntry{killerID: skills[winner].ID, killerIdx: winner}
		}
	}

	// Build surviving list and collect Overridden lists for survivors.
	overriddenBy := make(map[int][]string)
	for victimID, k := range killed {
		overriddenBy[k.killerIdx] = append(overriddenBy[k.killerIdx], victimID)
	}

	var survivors []Skill
	for i, s := range skills {
		if k, dead := killed[s.ID]; dead {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type:    "shadowed",
				Path:    s.FilePath,
				Message: fmt.Sprintf("skill %q shadowed by override from %q", s.ID, k.killerID),
			})
			continue
		}
		if extras := overriddenBy[i]; len(extras) > 0 {
			sort.Strings(extras)
			s.Overridden = append(s.Overridden, extras...)
		}
		survivors = append(survivors, s)
	}
	return survivors, diagnostics
}

// LoadSkillsOptions configures skill loading.
type LoadSkillsOptions struct {
	Cwd             string
	AgentDir        string
	SkillPaths      []string
	Roots           []SkillRoot // explicit per-root metadata; takes precedence over SkillPaths
	IncludeDefaults bool
}

// LoadSkills loads skills from all configured locations.
//
// Same-named skills from different origins coexist by default; the agent
// references them by ID (`<origin>__<name>`) when ambiguous, by bare name
// otherwise. `override: true` or `override: <full-id>` in a skill's
// frontmatter replaces other same-named skills (or one specific target).
func LoadSkills(opts LoadSkillsOptions) LoadSkillsResult {
	if opts.Cwd == "" {
		opts.Cwd, _ = os.Getwd()
	}

	var allSkills []Skill
	var allDiagnostics []ResourceDiagnostic

	realPathSet := make(map[string]bool)
	idSet := make(map[string]bool)

	addSkills := func(result LoadSkillsResult) {
		allDiagnostics = append(allDiagnostics, result.Diagnostics...)
		for _, skill := range result.Skills {
			// Resolve symlinks to detect duplicate files (the .fir/skills
			// symlink to pkg/core/builtin_skills, for example).
			realPath := skill.FilePath
			if resolved, err := filepath.EvalSymlinks(skill.FilePath); err == nil {
				realPath = resolved
			}
			if realPathSet[realPath] {
				continue
			}
			realPathSet[realPath] = true

			// Defensive: dedupe by ID too. Two distinct files producing the
			// same ID would be a bug (same origin + same name) but we skip
			// silently rather than corrupting the prompt.
			if idSet[skill.ID] {
				continue
			}
			idSet[skill.ID] = true

			allSkills = append(allSkills, skill)
		}
	}

	if opts.IncludeDefaults && opts.AgentDir != "" {
		addSkills(loadSkillsFromDirInternal(filepath.Join(opts.AgentDir, "skills"), "user", "user", true, map[string]bool{}))
		addSkills(loadSkillsFromDirInternal(filepath.Join(opts.Cwd, ".fir", "skills"), "project", "project", true, map[string]bool{}))
		addSkills(LoadBuiltinSkills())
	}

	// Roots take precedence over SkillPaths.
	if len(opts.Roots) > 0 {
		for _, r := range opts.Roots {
			addSkillsFromRoot(addSkills, opts.Cwd, r)
		}
	} else {
		for _, rawPath := range opts.SkillPaths {
			addSkillsFromRoot(addSkills, opts.Cwd, SkillRoot{
				Path:   rawPath,
				Source: "path",
				Origin: "path:" + filepath.Base(rawPath),
			})
		}
	}

	// Apply override semantics.
	survivors, overrideDiags := resolveOverrides(allSkills)
	allDiagnostics = append(allDiagnostics, overrideDiags...)

	// Emit informational duplicate-name diagnostics so listings/doctor can
	// surface coexistence.
	nameGroups := make(map[string][]string)
	for _, s := range survivors {
		nameGroups[s.Name] = append(nameGroups[s.Name], s.ID)
	}
	for name, ids := range nameGroups {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
			Type:    "duplicate-name",
			Message: fmt.Sprintf("skill %q present in %d origins; reference by ID: %s", name, len(ids), strings.Join(ids, ", ")),
		})
	}

	// Sort by ID for deterministic ordering (prompt-cache stability).
	sort.Slice(survivors, func(i, j int) bool { return survivors[i].ID < survivors[j].ID })

	return LoadSkillsResult{Skills: survivors, Diagnostics: allDiagnostics}
}

func addSkillsFromRoot(add func(LoadSkillsResult), cwd string, r SkillRoot) {
	resolvedPath := r.Path
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(cwd, resolvedPath)
	}
	source := r.Source
	if source == "" {
		source = "path"
	}
	origin := r.Origin
	if origin == "" {
		origin = "path:" + filepath.Base(resolvedPath)
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		add(LoadSkillsResult{Diagnostics: []ResourceDiagnostic{{
			Type:    "warning",
			Message: "skill path does not exist",
			Path:    resolvedPath,
		}}})
		return
	}

	if info.IsDir() {
		add(loadSkillsFromDirInternal(resolvedPath, source, origin, true, map[string]bool{}))
	} else if strings.HasSuffix(resolvedPath, ".md") {
		result := loadSkillFromFile(resolvedPath, source, origin)
		if result.Skill != nil {
			add(LoadSkillsResult{
				Skills:      []Skill{*result.Skill},
				Diagnostics: result.Diagnostics,
			})
		} else {
			add(LoadSkillsResult{Diagnostics: result.Diagnostics})
		}
	}
}
