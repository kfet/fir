// Ported from: packages/coding-agent/src/core/resource-loader.ts
// Upstream hash: 5c0ec26c
package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/config"
)

// ============================================================================
// Types
// ============================================================================

// PathMetadata describes where a resource came from and how it was discovered.
type PathMetadata struct {
	Source string `json:"source"` // "local", "cli", "auto", "package"
	Scope  string `json:"scope"`  // "user", "project", "temporary"
	Origin string `json:"origin"` // "top-level", "package"
}

// ResourcePackageResolver is implemented by the package manager to contribute
// resource paths without creating an import cycle between pkg/resources and pkg/pkg.
type ResourcePackageResolver interface {
	// ResolvePackageResources returns the paths of extensions, skills, and
	// themes contributed by all installed packages.
	ResolvePackageResources() (extensions, skills, themes []string, err error)

	// ResolvePackageContributions returns per-package attribution for each
	// resource path so that loaders can tag origin as `pkg:<source>`.
	// Implementations should return contributions for every installed package
	// whether or not it actually contains a given resource type.
	ResolvePackageContributions() ([]PackageContribution, error)
}

// PackageContribution describes one installed package and its resource paths.
type PackageContribution struct {
	Source      string   // package source string, e.g. "github.com/kfet/foo"
	Scope       string   // "user" or "project"
	InstallPath string   // root of the package on disk
	Extensions  []string // contributed extension paths
	Skills      []string // contributed skill paths
	Themes      []string // contributed theme paths
}

// ResourceExtensionPaths contains paths provided by extensions to extend resources.
type ResourceExtensionPaths struct {
	SkillPaths []PathEntry
}

// PathEntry pairs a path with its metadata.
type PathEntry struct {
	Path     string
	Metadata PathMetadata
}

// ResourceLoader provides access to loaded resources (skills, agents files, system prompt).
type ResourceLoader interface {
	GetSkills() ([]Skill, []ResourceDiagnostic)
	GetAgentsFiles() []AgentsFile
	GetSystemPrompt() string
	GetAppendSystemPrompt() []string
	GetPathMetadata() map[string]PathMetadata
	// GetPackageExtensionPaths returns executable extension scripts (.py/.sh)
	// contributed by installed packages. These are wired into the extension
	// discovery system, not the resource loader.
	GetPackageExtensionPaths() []string
	// GetPackageThemePaths returns theme JSON files contributed by installed
	// packages. These are added to the TUI theme search dirs.
	GetPackageThemePaths() []string
	ExtendResources(paths ResourceExtensionPaths)
	Reload() error
}

// AgentsFile represents a discovered AGENTS.md or CLAUDE.md file.
type AgentsFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ============================================================================
// DefaultResourceLoader
// ============================================================================

// ResourceLoaderOptions configures the DefaultResourceLoader.
type ResourceLoaderOptions struct {
	Cwd      string
	AgentDir string

	SettingsManager *config.SettingsManager

	PackageResolver ResourcePackageResolver

	AdditionalSkillPaths []string

	NoSkills bool

	SystemPrompt       string
	AppendSystemPrompt string
}

// DefaultResourceLoader implements ResourceLoader using the filesystem.
type DefaultResourceLoader struct {
	cwd      string
	agentDir string

	settingsManager *config.SettingsManager
	pkgResolver     ResourcePackageResolver

	additionalSkillPaths []string

	noSkills bool

	systemPromptSource       string
	appendSystemPromptSource string

	// Loaded state
	skills             []Skill
	skillDiagnostics   []ResourceDiagnostic
	agentsFiles        []AgentsFile
	systemPrompt       string
	appendSystemPrompt []string
	pathMetadata       map[string]PathMetadata

	lastSkillPaths []string

	// Package-contributed paths for systems outside the resource loader
	// (extensions are handled by pkg/extension; themes by the TUI theme loader).
	pkgExtensionPaths []string
	pkgThemePaths     []string
}

// NewResourceLoader creates a new DefaultResourceLoader with the given options.
func NewResourceLoader(opts ResourceLoaderOptions) *DefaultResourceLoader {
	cwd := opts.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	agentDir := opts.AgentDir
	if agentDir == "" {
		home, _ := os.UserHomeDir()
		agentDir = filepath.Join(home, ".fir", "agent")
	}

	sm := opts.SettingsManager
	if sm == nil {
		sm = config.NewSettingsManager(cwd, agentDir)
	}

	return &DefaultResourceLoader{
		cwd:                      cwd,
		agentDir:                 agentDir,
		settingsManager:          sm,
		pkgResolver:              opts.PackageResolver,
		additionalSkillPaths:     opts.AdditionalSkillPaths,
		noSkills:                 opts.NoSkills,
		systemPromptSource:       opts.SystemPrompt,
		appendSystemPromptSource: opts.AppendSystemPrompt,
		pathMetadata:             make(map[string]PathMetadata),
	}
}

// ============================================================================
// ResourceLoader interface implementation
// ============================================================================

func (r *DefaultResourceLoader) GetSkills() ([]Skill, []ResourceDiagnostic) {
	return r.skills, r.skillDiagnostics
}

func (r *DefaultResourceLoader) GetAgentsFiles() []AgentsFile {
	return r.agentsFiles
}

func (r *DefaultResourceLoader) GetSystemPrompt() string {
	return r.systemPrompt
}

func (r *DefaultResourceLoader) GetAppendSystemPrompt() []string {
	return r.appendSystemPrompt
}

func (r *DefaultResourceLoader) GetPathMetadata() map[string]PathMetadata {
	return r.pathMetadata
}

func (r *DefaultResourceLoader) GetPackageExtensionPaths() []string {
	if r.pkgExtensionPaths == nil {
		return nil
	}
	out := make([]string, len(r.pkgExtensionPaths))
	copy(out, r.pkgExtensionPaths)
	return out
}

func (r *DefaultResourceLoader) GetPackageThemePaths() []string {
	if r.pkgThemePaths == nil {
		return nil
	}
	out := make([]string, len(r.pkgThemePaths))
	copy(out, r.pkgThemePaths)
	return out
}

func (r *DefaultResourceLoader) ExtendResources(paths ResourceExtensionPaths) {
	if len(paths.SkillPaths) > 0 {
		skillPaths := r.normalizeExtensionPaths(paths.SkillPaths)
		newPaths := make([]string, len(skillPaths))
		for i, e := range skillPaths {
			newPaths[i] = e.Path
		}
		r.lastSkillPaths = mergePaths(r.cwd, r.lastSkillPaths, newPaths)
		r.updateSkillsFromPaths(r.lastSkillPaths, skillPaths)
	}
}

// Reload reloads all resources from disk.
func (r *DefaultResourceLoader) Reload() error {
	r.pathMetadata = make(map[string]PathMetadata)

	// Build typed roots for skills (preserves origin → package source mapping).
	var skillRoots []SkillRoot
	if !r.noSkills {
		// Defaults: user agent dir, then project .fir, then builtin (handled
		// separately inside LoadSkills via IncludeDefaults).
		if dir := filepath.Join(r.agentDir, "skills"); dirExists(dir) {
			skillRoots = append(skillRoots, SkillRoot{Path: dir, Source: "user", Origin: "user"})
		}
		if dir := filepath.Join(r.cwd, config.ConfigDirName, "skills"); dirExists(dir) {
			skillRoots = append(skillRoots, SkillRoot{Path: dir, Source: "project", Origin: "project"})
		}
	}
	// Settings + CLI extra paths get path:<basename> origins.
	for _, p := range r.additionalSkillPaths {
		skillRoots = append(skillRoots, SkillRoot{Path: p, Source: "path", Origin: "path:" + filepath.Base(p)})
	}
	if r.settingsManager != nil {
		for _, p := range r.settingsManager.GetSkillPaths() {
			skillRoots = append(skillRoots, SkillRoot{Path: p, Source: "path", Origin: "path:" + filepath.Base(p)})
		}
	}

	// Append package resources if a resolver is configured.
	var pkgSkillEntries []PathEntry
	if r.pkgResolver != nil {
		// Per-package attribution for skills (origin = pkg:<source>).
		contribs, contribErr := r.pkgResolver.ResolvePackageContributions()
		if contribErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: package contributions: %v\n", contribErr)
		} else {
			for _, c := range contribs {
				origin := "pkg:" + c.Source
				for _, p := range c.Skills {
					skillRoots = append(skillRoots, SkillRoot{Path: p, Source: "package", Origin: origin})
					pkgSkillEntries = append(pkgSkillEntries, PathEntry{
						Path:     p,
						Metadata: PathMetadata{Source: "package", Scope: c.Scope, Origin: "package"},
					})
				}
			}
		}

		// Extensions/themes still go through the legacy flat list
		// until they get the same Origin/ID treatment in a follow-up.
		pkgExtensions, _, pkgThemes, err := r.pkgResolver.ResolvePackageResources()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: package resources: %v\n", err)
		} else {
			r.pkgExtensionPaths = pkgExtensions
			r.pkgThemePaths = pkgThemes
		}
	}

	// Track flat skill paths for back-compat callers/extension metadata.
	flatSkillPaths := make([]string, 0, len(skillRoots))
	for _, sr := range skillRoots {
		flatSkillPaths = append(flatSkillPaths, sr.Path)
	}
	r.lastSkillPaths = flatSkillPaths
	r.updateSkillsFromRoots(skillRoots, pkgSkillEntries)

	// Load AGENTS.md / CLAUDE.md files
	r.agentsFiles = loadProjectContextFiles(r.cwd, r.agentDir)

	// Load system prompt
	r.systemPrompt = r.resolveSystemPrompt()

	// Load append system prompt
	r.appendSystemPrompt = r.resolveAppendSystemPrompt()

	return nil
}

// scopeForPath infers whether a path belongs to the project or user scope.
func (r *DefaultResourceLoader) scopeForPath(p string) string {
	abs, _ := filepath.Abs(p)
	if isUnderPath(abs, filepath.Join(r.cwd, config.ConfigDirName)) {
		return "project"
	}
	return "user"
}

// classifySkillSource returns a meaningful Source label for a loaded skill
// based on where its file lives. Builtin skills keep "builtin"; everything
// else is bucketed as "project" (under <cwd>/.fir), "user" (under agentDir
// or ~/.fir), "package" (installed fir package), or falls back to "path".
func (r *DefaultResourceLoader) classifySkillSource(s Skill) string {
	if s.Source == "builtin" {
		return "builtin"
	}
	abs, err := filepath.Abs(s.FilePath)
	if err != nil {
		abs = s.FilePath
	}

	projectFir := filepath.Join(r.cwd, config.ConfigDirName)
	userFir := r.agentDir
	if home, err := os.UserHomeDir(); err == nil {
		homeFir := filepath.Join(home, config.ConfigDirName)
		// Packages installed without --local live under ~/.fir/packages
		if isUnderPath(abs, filepath.Join(homeFir, "packages")) {
			return "package"
		}
		// Treat anything else under ~/.fir as user-scoped.
		if isUnderPath(abs, homeFir) {
			return "user"
		}
	}
	// Project-local packages: <cwd>/.fir/packages
	if isUnderPath(abs, filepath.Join(projectFir, "packages")) {
		return "package"
	}
	if isUnderPath(abs, projectFir) {
		return "project"
	}
	if userFir != "" && isUnderPath(abs, userFir) {
		return "user"
	}
	return "path"
}

// ============================================================================
// Internal helpers
// ============================================================================

func (r *DefaultResourceLoader) updateSkillsFromPaths(paths []string, extensionPaths []PathEntry) {
	roots := make([]SkillRoot, 0, len(paths))
	for _, p := range paths {
		roots = append(roots, SkillRoot{Path: p, Source: "path", Origin: "path:" + filepath.Base(p)})
	}
	r.updateSkillsFromRoots(roots, extensionPaths)
}

func (r *DefaultResourceLoader) updateSkillsFromRoots(roots []SkillRoot, extensionPaths []PathEntry) {
	if r.noSkills && len(roots) == 0 {
		r.skills = nil
		r.skillDiagnostics = nil
		return
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             r.cwd,
		AgentDir:        r.agentDir,
		Roots:           roots,
		IncludeDefaults: false, // We pre-include defaults in roots
	})
	r.skills = result.Skills
	r.skillDiagnostics = result.Diagnostics

	// Append builtin skills that aren't already loaded by ID (lowest priority).
	if !r.noSkills {
		existing := make(map[string]bool, len(r.skills))
		for _, s := range r.skills {
			existing[s.ID] = true
		}
		builtins := LoadBuiltinSkills()
		r.skillDiagnostics = append(r.skillDiagnostics, builtins.Diagnostics...)
		// Apply override semantics with current skills + builtins together so
		// `override: true` declared by a user/project skill correctly shadows
		// a builtin of the same name.
		var combined []Skill
		combined = append(combined, r.skills...)
		for _, s := range builtins.Skills {
			if !existing[s.ID] {
				combined = append(combined, s)
			}
		}
		survivors, diags := resolveOverrides(combined)
		r.skills = survivors
		r.skillDiagnostics = append(r.skillDiagnostics, diags...)
	}
	// Reclassify Source by FilePath location so listings show meaningful
	// scopes ("user", "project", "package") instead of the generic "path"
	// that LoadSkills assigns to anything coming via SkillPaths.
	for i := range r.skills {
		r.skills[i].Source = r.classifySkillSource(r.skills[i])
	}
	// Sort by ID for deterministic ordering.
	sort.Slice(r.skills, func(i, j int) bool { return r.skills[i].ID < r.skills[j].ID })
	r.applyExtensionMetadata(extensionPaths, skillFilePaths(r.skills))

	for _, skill := range r.skills {
		r.addDefaultMetadataForPath(skill.FilePath)
	}
}

func (r *DefaultResourceLoader) normalizeExtensionPaths(entries []PathEntry) []PathEntry {
	result := make([]PathEntry, len(entries))
	for i, e := range entries {
		result[i] = PathEntry{
			Path:     resolveResourcePath(r.cwd, e.Path),
			Metadata: e.Metadata,
		}
	}
	return result
}

func (r *DefaultResourceLoader) resolveSystemPrompt() string {
	// Check explicit source first
	source := r.systemPromptSource
	if source == "" {
		source = r.discoverSystemPromptFile()
	}
	if source == "" {
		return ""
	}
	return resolvePromptInput(source)
}

func (r *DefaultResourceLoader) resolveAppendSystemPrompt() []string {
	source := r.appendSystemPromptSource
	if source == "" {
		source = r.discoverAppendSystemPromptFile()
	}
	if source == "" {
		return nil
	}
	resolved := resolvePromptInput(source)
	if resolved == "" {
		return nil
	}
	return []string{resolved}
}

func (r *DefaultResourceLoader) discoverSystemPromptFile() string {
	projectPath := filepath.Join(r.cwd, config.ConfigDirName, "SYSTEM.md")
	if fileExists(projectPath) {
		return projectPath
	}
	globalPath := filepath.Join(r.agentDir, "SYSTEM.md")
	if fileExists(globalPath) {
		return globalPath
	}
	return ""
}

func (r *DefaultResourceLoader) discoverAppendSystemPromptFile() string {
	projectPath := filepath.Join(r.cwd, config.ConfigDirName, "APPEND_SYSTEM.md")
	if fileExists(projectPath) {
		return projectPath
	}
	globalPath := filepath.Join(r.agentDir, "APPEND_SYSTEM.md")
	if fileExists(globalPath) {
		return globalPath
	}
	return ""
}

func (r *DefaultResourceLoader) applyExtensionMetadata(entries []PathEntry, resourcePaths []string) {
	if len(entries) == 0 {
		return
	}

	normalized := make([]PathEntry, len(entries))
	for i, e := range entries {
		abs, _ := filepath.Abs(e.Path)
		normalized[i] = PathEntry{Path: abs, Metadata: e.Metadata}
		if _, ok := r.pathMetadata[abs]; !ok {
			r.pathMetadata[abs] = e.Metadata
		}
	}

	for _, rp := range resourcePaths {
		abs, _ := filepath.Abs(rp)
		if _, ok := r.pathMetadata[abs]; ok {
			continue
		}
		if _, ok := r.pathMetadata[rp]; ok {
			continue
		}
		for _, entry := range normalized {
			if abs == entry.Path || strings.HasPrefix(abs, entry.Path+string(filepath.Separator)) {
				r.pathMetadata[abs] = entry.Metadata
				break
			}
		}
	}
}

func (r *DefaultResourceLoader) addDefaultMetadataForPath(filePath string) {
	if filePath == "" || strings.HasPrefix(filePath, "<") {
		return
	}

	abs, _ := filepath.Abs(filePath)
	if _, ok := r.pathMetadata[abs]; ok {
		return
	}
	if _, ok := r.pathMetadata[filePath]; ok {
		return
	}

	agentRoots := []string{
		filepath.Join(r.agentDir, "skills"),
	}
	projectRoots := []string{
		filepath.Join(r.cwd, config.ConfigDirName, "skills"),
	}

	for _, root := range agentRoots {
		if isUnderPath(abs, root) {
			r.pathMetadata[abs] = PathMetadata{Source: "local", Scope: "user", Origin: "top-level"}
			return
		}
	}

	for _, root := range projectRoots {
		if isUnderPath(abs, root) {
			r.pathMetadata[abs] = PathMetadata{Source: "local", Scope: "project", Origin: "top-level"}
			return
		}
	}
}

// ============================================================================
// Package-level helpers
// ============================================================================

func defaultSkillPaths(cwd, agentDir string) []string {
	var paths []string
	// Global skill dir
	globalDir := filepath.Join(agentDir, "skills")
	if dirExists(globalDir) {
		paths = append(paths, globalDir)
	}
	// Project skill dir
	projectDir := filepath.Join(cwd, config.ConfigDirName, "skills")
	if dirExists(projectDir) {
		paths = append(paths, projectDir)
	}
	return paths
}

// loadProjectContextFiles loads AGENTS.md / CLAUDE.md files from ancestor directories.
func loadProjectContextFiles(cwd, agentDir string) []AgentsFile {
	candidates := []string{"AGENTS.md", "CLAUDE.md"}
	var files []AgentsFile
	seenPaths := make(map[string]bool)

	// Global context file
	if f := loadContextFileFromDir(agentDir, candidates); f != nil {
		files = append(files, *f)
		seenPaths[f.Path] = true
	}

	// Walk up from cwd to root, collecting files bottom-up, then reverse
	var ancestorFiles []AgentsFile
	currentDir, _ := filepath.Abs(cwd)
	root := filepath.VolumeName(currentDir) + string(filepath.Separator)

	for {
		if f := loadContextFileFromDir(currentDir, candidates); f != nil {
			if !seenPaths[f.Path] {
				ancestorFiles = append([]AgentsFile{*f}, ancestorFiles...)
				seenPaths[f.Path] = true
			}
		}
		if currentDir == root {
			break
		}
		parent := filepath.Dir(currentDir)
		if parent == currentDir {
			break
		}
		currentDir = parent
	}

	files = append(files, ancestorFiles...)
	return files
}

func loadContextFileFromDir(dir string, candidates []string) *AgentsFile {
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			return &AgentsFile{Path: path, Content: string(data)}
		}
	}
	return nil
}

// resolvePromptInput reads a file if it exists, otherwise returns the string as-is.
func resolvePromptInput(input string) string {
	if input == "" {
		return ""
	}
	if fileExists(input) {
		data, err := os.ReadFile(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not read prompt file %s: %v\n", input, err)
			return input
		}
		return string(data)
	}
	return input
}

// mergePaths merges two path lists, resolving relative paths and deduplicating.
func mergePaths(cwd string, primary, additional []string) []string {
	var merged []string
	seen := make(map[string]bool)

	for _, p := range append(primary, additional...) {
		resolved := resolveResourcePath(cwd, p)
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		merged = append(merged, resolved)
	}
	return merged
}

// ResolveResourcePath expands ~ and resolves relative paths against cwd.
// Exported for callers that need the same resolution semantics as resource paths.
func ResolveResourcePath(cwd, p string) string {
	return resolveResourcePath(cwd, p)
}

// ResolveSettingsExtensionPaths resolves extensionPaths entries from settings
// (merged global + project) against cwd, matching the semantics used for
// skills/themes path settings. Relative paths resolve against cwd,
// '~' expands to $HOME, and absolute paths pass through unchanged.
// Duplicates are removed while preserving order.
//
// Shared by the CLI/TUI path (cmd/fir) and ACP mode so both wire the same
// settings-provided extension directories.
func ResolveSettingsExtensionPaths(cwd string, sm *config.SettingsManager) []string {
	if sm == nil {
		return nil
	}
	raw := sm.GetExtensionPaths()
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, p := range raw {
		r := ResolveResourcePath(cwd, p)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// resolveResourcePath expands ~ and resolves relative paths.
func resolveResourcePath(cwd, p string) string {
	trimmed := strings.TrimSpace(p)
	home, _ := os.UserHomeDir()

	if trimmed == "~" {
		return home
	}
	if strings.HasPrefix(trimmed, "~/") {
		return filepath.Join(home, trimmed[2:])
	}
	if strings.HasPrefix(trimmed, "~") {
		return filepath.Join(home, trimmed[1:])
	}

	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	return filepath.Join(cwd, trimmed)
}

func isUnderPath(target, root string) bool {
	absRoot, _ := filepath.Abs(root)
	if target == absRoot {
		return true
	}
	prefix := absRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func skillFilePaths(skills []Skill) []string {
	paths := make([]string, len(skills))
	for i, s := range skills {
		paths[i] = s.FilePath
	}
	return paths
}
