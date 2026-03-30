package pkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SettingsBackend abstracts the settings persistence needed by Manager.
// *config.SettingsManager satisfies this interface once the corresponding
// Get/SetGlobalPackages and GetProjectPackages methods are added.
type SettingsBackend interface {
	GetGlobalPackages() []any
	SetGlobalPackages([]any)
	GetProjectPackages() []any
	SetProjectPackages([]any)
}

// InstalledPackage describes a single registered package and its discovered resources.
type InstalledPackage struct {
	Source      *Source
	Scope       string // "user" | "project"
	InstallPath string
	Resources   *PackageResources
}

// ResolvedResources is the aggregate of all resources from all installed packages.
type ResolvedResources struct {
	Extensions []string
	Skills     []string
	Prompts    []string
	Themes     []string
}

// Manager manages installation, removal, and resolution of fir packages.
type Manager struct {
	agentDir string // e.g. ~/.config/fir
	cwd      string // project working directory
	sm       SettingsBackend
}

// New creates a Manager.
// agentDir is the user-level agent config dir (e.g. ~/.config/fir).
// cwd is the project working directory.
func New(agentDir, cwd string, sm SettingsBackend) *Manager {
	return &Manager{agentDir: agentDir, cwd: cwd, sm: sm}
}

// Install parses source, clones (git) or verifies (local), then registers it.
// If local is true the package is added to project scope; otherwise user scope.
func (m *Manager) Install(source string, local bool) error {
	src, err := ParseSource(source)
	if err != nil {
		return err
	}

	if src.Type == "git" {
		dest := m.gitInstallPath(src, local)
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			if src.SubDir != "" {
				fmt.Printf("Sparse-cloning %s (subdir: %s)...\n", src.URL, src.SubDir)
				if err := SparseCloneRef(src.URL, src.Ref, src.SubDir, dest); err != nil {
					return err
				}
			} else {
				fmt.Printf("Cloning %s...\n", src.URL)
				if err := CloneRef(src.URL, src.Ref, dest); err != nil {
					return err
				}
			}
		} else {
			fmt.Printf("Already cloned at %s, pulling...\n", dest)
			if err := Pull(dest); err != nil {
				return err
			}
		}
	} else {
		// Local: just verify the path exists.
		if _, err := os.Stat(src.Local); err != nil {
			return fmt.Errorf("local path %q does not exist: %w", src.Local, err)
		}
	}

	// Register in settings.
	if err := m.addPackage(source, local); err != nil {
		return err
	}

	// Print discovered resources.
	installPath := m.installPath(src, local)
	res, err := ScanPackageResources(installPath)
	if err == nil {
		fmt.Printf("Discovered: %d skill(s), %d extension(s), %d prompt(s), %d theme(s)\n",
			len(res.Skills), len(res.Extensions), len(res.Prompts), len(res.Themes))
	}
	return nil
}

// Uninstall removes the package from settings and deletes the cloned directory (git only).
func (m *Manager) Uninstall(source string, local bool) error {
	src, err := ParseSource(source)
	if err != nil {
		return err
	}

	if err := m.removePackage(source, local); err != nil {
		return err
	}

	if src.Type == "git" {
		dest := m.gitInstallPath(src, local)
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("removing clone at %s: %w", dest, err)
		}
	}

	fmt.Printf("Removed %s\n", source)
	return nil
}

// Update pulls the latest commits for one or all installed git packages.
// If source is "" all packages are updated.
func (m *Manager) Update(source string) error {
	pkgs, err := m.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range pkgs {
		if source != "" && p.Source.Raw != source {
			continue
		}
		if p.Source.Type != "git" {
			continue
		}
		fmt.Printf("Updating %s... ", p.Source.Raw)
		if err := Pull(p.InstallPath); err != nil {
			fmt.Printf("error: %v\n", err)
			errs = append(errs, err)
		} else {
			fmt.Println("done")
		}
	}
	return errors.Join(errs...)
}

// List returns all registered packages with their install paths and scanned resources.
func (m *Manager) List() ([]InstalledPackage, error) {
	var result []InstalledPackage

	// User-scope packages.
	for _, raw := range m.sm.GetGlobalPackages() {
		ip, err := m.resolveEntry(raw, "user")
		if err != nil {
			return nil, err
		}
		result = append(result, ip)
	}

	// Project-scope packages.
	for _, raw := range m.sm.GetProjectPackages() {
		ip, err := m.resolveEntry(raw, "project")
		if err != nil {
			return nil, err
		}
		result = append(result, ip)
	}

	return result, nil
}

// Resolve aggregates resources from all installed packages.
func (m *Manager) Resolve() (*ResolvedResources, error) {
	pkgs, err := m.List()
	if err != nil {
		return nil, err
	}
	rr := &ResolvedResources{}
	for _, p := range pkgs {
		if p.Resources == nil {
			continue
		}
		rr.Extensions = append(rr.Extensions, p.Resources.Extensions...)
		rr.Skills = append(rr.Skills, p.Resources.Skills...)
		rr.Prompts = append(rr.Prompts, p.Resources.Prompts...)
		rr.Themes = append(rr.Themes, p.Resources.Themes...)
	}
	return rr, nil
}

// --- internal helpers ---

// gitInstallPath returns the directory where a git package should be cloned.
func (m *Manager) gitInstallPath(src *Source, projectScope bool) string {
	if projectScope {
		return filepath.Join(m.cwd, ".fir", "packages", "git", src.Host, src.Path)
	}
	return filepath.Join(m.agentDir, "packages", "git", src.Host, src.Path)
}

// installPath returns the effective directory for a parsed source.
// For git sources with a SubDir, this points into the subdirectory within the clone.
func (m *Manager) installPath(src *Source, projectScope bool) string {
	if src.Type == "local" {
		return src.Local
	}
	base := m.gitInstallPath(src, projectScope)
	if src.SubDir != "" {
		return filepath.Join(base, src.SubDir)
	}
	return base
}

// addPackage appends a source string to the appropriate settings scope,
// deduplicating by the canonical raw source string.
func (m *Manager) addPackage(source string, projectScope bool) error {
	if projectScope {
		pkgs := m.sm.GetProjectPackages()
		if containsPackage(pkgs, source) {
			return nil
		}
		m.sm.SetProjectPackages(append(pkgs, source))
	} else {
		pkgs := m.sm.GetGlobalPackages()
		if containsPackage(pkgs, source) {
			return nil
		}
		m.sm.SetGlobalPackages(append(pkgs, source))
	}
	return nil
}

// removePackage removes a source string from the appropriate settings scope.
func (m *Manager) removePackage(source string, projectScope bool) error {
	if projectScope {
		pkgs := m.sm.GetProjectPackages()
		m.sm.SetProjectPackages(filterPackage(pkgs, source))
	} else {
		pkgs := m.sm.GetGlobalPackages()
		m.sm.SetGlobalPackages(filterPackage(pkgs, source))
	}
	return nil
}

// containsPackage checks whether source is already registered in the packages
// slice. Comparison is by canonical identity (Host+"/"+Path for git, resolved
// path for local) so different spellings of the same repo don't create duplicates.
func containsPackage(pkgs []any, source string) bool {
	newSrc, err := ParseSource(source)
	if err != nil {
		return false
	}
	newID := sourceIdentity(newSrc)
	for _, p := range pkgs {
		existing := entrySource(p)
		if existing == "" {
			continue
		}
		existSrc, err := ParseSource(existing)
		if err != nil {
			if existing == source {
				return true
			}
			continue
		}
		if sourceIdentity(existSrc) == newID {
			return true
		}
	}
	return false
}

// sourceIdentity returns a canonical string for deduplication.
// For git: "git:host/path[/subdir]". For local: "local:<abs-path>".
func sourceIdentity(src *Source) string {
	if src.Type == "git" {
		id := "git:" + src.Host + "/" + src.Path
		if src.SubDir != "" {
			id += "/" + src.SubDir
		}
		return id
	}
	return "local:" + src.Local
}

// filterPackage returns pkgs without the entry matching source.
func filterPackage(pkgs []any, source string) []any {
	out := pkgs[:0:0]
	for _, p := range pkgs {
		if entrySource(p) != source {
			out = append(out, p)
		}
	}
	return out
}

// entrySource extracts the source string from a packages entry (string or object).
func entrySource(entry any) string {
	switch v := entry.(type) {
	case string:
		return v
	case map[string]any:
		if s, ok := v["source"].(string); ok {
			return s
		}
	}
	// Fallback: round-trip through JSON to handle map[string]interface{} decoded
	// from JSON that may have come in as json.RawMessage or similar.
	data, err := json.Marshal(entry)
	if err != nil {
		return ""
	}
	var obj struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.Source != "" {
		return obj.Source
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s
	}
	return ""
}

// resolveEntry builds an InstalledPackage from a raw settings entry.
func (m *Manager) resolveEntry(entry any, scope string) (InstalledPackage, error) {
	rawSource := entrySource(entry)
	if rawSource == "" {
		return InstalledPackage{}, fmt.Errorf("unreadable package entry: %v", entry)
	}

	src, err := ParseSource(rawSource)
	if err != nil {
		return InstalledPackage{}, fmt.Errorf("package %q: %w", rawSource, err)
	}

	projectScope := scope == "project"
	installPath := m.installPath(src, projectScope)

	// Scan resources — best-effort; ignore errors for missing dirs.
	var resources *PackageResources
	if _, err := os.Stat(installPath); err == nil {
		resources, _ = ScanPackageResources(installPath)
	}

	return InstalledPackage{
		Source:      src,
		Scope:       scope,
		InstallPath: installPath,
		Resources:   resources,
	}, nil
}

// gitDirBase returns "host/path" used as the storage sub-path.
// Exported for test convenience.
func gitDirBase(src *Source) string {
	return src.Host + string(filepath.Separator) + strings.ReplaceAll(src.Path, "/", string(filepath.Separator))
}

// ResolvePackageResources implements resources.ResourcePackageResolver.
// It returns the aggregate extension, skill, prompt, and theme paths
// from all installed packages so the resource loader can include them
// without importing pkg/pkg directly.
func (m *Manager) ResolvePackageResources() (extensions, skills, prompts, themes []string, err error) {
	rr, err := m.Resolve()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return rr.Extensions, rr.Skills, rr.Prompts, rr.Themes, nil
}
