// Ported from: packages/coding-agent/src/core/resource-loader.ts
// Upstream hash: 5c0ec26c
package resources

import (
	"fmt"
	"os"
	"path/filepath"
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
	// ResolvePackageResources returns the paths of extensions, skills, prompts,
	// and themes contributed by all installed packages.
	ResolvePackageResources() (extensions, skills, prompts, themes []string, err error)
}

// ResourceExtensionPaths contains paths provided by extensions to extend resources.
type ResourceExtensionPaths struct {
	SkillPaths  []PathEntry
	PromptPaths []PathEntry
}

// PathEntry pairs a path with its metadata.
type PathEntry struct {
	Path     string
	Metadata PathMetadata
}

// ResourceLoader provides access to loaded resources (skills, prompts, agents files, system prompt).
type ResourceLoader interface {
	GetSkills() ([]Skill, []ResourceDiagnostic)
	GetPrompts() ([]PromptTemplate, []ResourceDiagnostic)
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

	AdditionalSkillPaths          []string
	AdditionalPromptTemplatePaths []string

	NoSkills          bool
	NoPromptTemplates bool

	SystemPrompt       string
	AppendSystemPrompt string
}

// DefaultResourceLoader implements ResourceLoader using the filesystem.
type DefaultResourceLoader struct {
	cwd      string
	agentDir string

	settingsManager *config.SettingsManager
	pkgResolver     ResourcePackageResolver

	additionalSkillPaths  []string
	additionalPromptPaths []string

	noSkills  bool
	noPrompts bool

	systemPromptSource       string
	appendSystemPromptSource string

	// Loaded state
	skills             []Skill
	skillDiagnostics   []ResourceDiagnostic
	prompts            []PromptTemplate
	promptDiagnostics  []ResourceDiagnostic
	agentsFiles        []AgentsFile
	systemPrompt       string
	appendSystemPrompt []string
	pathMetadata       map[string]PathMetadata

	lastSkillPaths  []string
	lastPromptPaths []string

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
		additionalPromptPaths:    opts.AdditionalPromptTemplatePaths,
		noSkills:                 opts.NoSkills,
		noPrompts:                opts.NoPromptTemplates,
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

func (r *DefaultResourceLoader) GetPrompts() ([]PromptTemplate, []ResourceDiagnostic) {
	return r.prompts, r.promptDiagnostics
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

	if len(paths.PromptPaths) > 0 {
		promptPaths := r.normalizeExtensionPaths(paths.PromptPaths)
		newPaths := make([]string, len(promptPaths))
		for i, e := range promptPaths {
			newPaths[i] = e.Path
		}
		r.lastPromptPaths = mergePaths(r.cwd, r.lastPromptPaths, newPaths)
		r.updatePromptsFromPaths(r.lastPromptPaths, promptPaths)
	}
}

// Reload reloads all resources from disk.
func (r *DefaultResourceLoader) Reload() error {
	r.pathMetadata = make(map[string]PathMetadata)

	// Load skills — merge defaults, settings paths, and CLI paths.
	skillPaths := r.additionalSkillPaths
	if !r.noSkills {
		skillPaths = mergePaths(r.cwd, defaultSkillPaths(r.cwd, r.agentDir), skillPaths)
	}
	if r.settingsManager != nil {
		skillPaths = mergePaths(r.cwd, skillPaths, r.settingsManager.GetSkillPaths())
	}

	// Load prompt templates — merge defaults, settings paths, and CLI paths.
	promptPaths := r.additionalPromptPaths
	if !r.noPrompts {
		promptPaths = mergePaths(r.cwd, defaultPromptPaths(r.cwd, r.agentDir), promptPaths)
	}
	if r.settingsManager != nil {
		promptPaths = mergePaths(r.cwd, promptPaths, r.settingsManager.GetPromptPaths())
	}

	// Append package resources if a resolver is configured.
	var pkgSkillEntries, pkgPromptEntries []PathEntry
	if r.pkgResolver != nil {
		pkgExtensions, pkgSkills, pkgPrompts, pkgThemes, err := r.pkgResolver.ResolvePackageResources()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: package resources: %v\n", err)
		} else {
			// Store extension and theme paths for callers that handle those systems.
			r.pkgExtensionPaths = pkgExtensions
			r.pkgThemePaths = pkgThemes

			for _, p := range pkgSkills {
				meta := PathMetadata{Source: "package", Scope: r.scopeForPath(p), Origin: "package"}
				pkgSkillEntries = append(pkgSkillEntries, PathEntry{Path: p, Metadata: meta})
				skillPaths = append(skillPaths, p)
			}
			for _, p := range pkgPrompts {
				meta := PathMetadata{Source: "package", Scope: r.scopeForPath(p), Origin: "package"}
				pkgPromptEntries = append(pkgPromptEntries, PathEntry{Path: p, Metadata: meta})
				promptPaths = append(promptPaths, p)
			}
		}
	}

	r.lastSkillPaths = skillPaths
	r.updateSkillsFromPaths(skillPaths, pkgSkillEntries)

	r.lastPromptPaths = promptPaths
	r.updatePromptsFromPaths(promptPaths, pkgPromptEntries)

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

// ============================================================================
// Internal helpers
// ============================================================================

func (r *DefaultResourceLoader) updateSkillsFromPaths(paths []string, extensionPaths []PathEntry) {
	if r.noSkills && len(paths) == 0 {
		r.skills = nil
		r.skillDiagnostics = nil
		return
	}

	result := LoadSkills(LoadSkillsOptions{
		Cwd:             r.cwd,
		AgentDir:        r.agentDir,
		SkillPaths:      paths,
		IncludeDefaults: false, // We pre-include defaults in paths
	})
	r.skills = result.Skills
	r.skillDiagnostics = result.Diagnostics

	// Append builtin skills that aren't already loaded (lowest priority).
	if !r.noSkills {
		existing := make(map[string]bool, len(r.skills))
		for _, s := range r.skills {
			existing[s.Name] = true
		}
		builtins := LoadBuiltinSkills()
		r.skillDiagnostics = append(r.skillDiagnostics, builtins.Diagnostics...)
		for _, s := range builtins.Skills {
			if !existing[s.Name] {
				r.skills = append(r.skills, s)
			}
		}
	}
	r.applyExtensionMetadata(extensionPaths, skillFilePaths(r.skills))

	for _, skill := range r.skills {
		r.addDefaultMetadataForPath(skill.FilePath)
	}
}

func (r *DefaultResourceLoader) updatePromptsFromPaths(paths []string, extensionPaths []PathEntry) {
	if r.noPrompts && len(paths) == 0 {
		r.prompts = nil
		r.promptDiagnostics = nil
		return
	}

	prompts := LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd:             r.cwd,
		AgentDir:        r.agentDir,
		PromptPaths:     paths,
		IncludeDefaults: false,
	})
	deduped, diags := dedupePrompts(prompts)
	r.prompts = deduped
	r.promptDiagnostics = diags
	r.applyExtensionMetadata(extensionPaths, promptFilePaths(r.prompts))

	for _, prompt := range r.prompts {
		r.addDefaultMetadataForPath(prompt.FilePath)
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
		filepath.Join(r.agentDir, "prompts"),
	}
	projectRoots := []string{
		filepath.Join(r.cwd, config.ConfigDirName, "skills"),
		filepath.Join(r.cwd, config.ConfigDirName, "prompts"),
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

func defaultPromptPaths(cwd, agentDir string) []string {
	var paths []string
	globalDir := filepath.Join(agentDir, "prompts")
	if dirExists(globalDir) {
		paths = append(paths, globalDir)
	}
	projectDir := filepath.Join(cwd, config.ConfigDirName, "prompts")
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

func promptFilePaths(prompts []PromptTemplate) []string {
	paths := make([]string, len(prompts))
	for i, p := range prompts {
		paths[i] = p.FilePath
	}
	return paths
}

// dedupePrompts removes duplicate prompt templates by name, keeping the first occurrence.
func dedupePrompts(prompts []PromptTemplate) ([]PromptTemplate, []ResourceDiagnostic) {
	seen := make(map[string]PromptTemplate)
	var diags []ResourceDiagnostic

	for _, p := range prompts {
		if existing, ok := seen[p.Name]; ok {
			diags = append(diags, ResourceDiagnostic{
				Type:    "collision",
				Message: fmt.Sprintf("name \"/%s\" collision", p.Name),
				Path:    p.FilePath,
			})
			_ = existing // keep first
		} else {
			seen[p.Name] = p
		}
	}

	result := make([]PromptTemplate, 0, len(seen))
	// Preserve order: re-iterate prompts, include only first occurrences
	added := make(map[string]bool)
	for _, p := range prompts {
		if !added[p.Name] {
			if _, ok := seen[p.Name]; ok {
				result = append(result, seen[p.Name])
				added[p.Name] = true
			}
		}
	}
	return result, diags
}
