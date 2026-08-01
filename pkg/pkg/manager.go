package pkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/resources"
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
		// Subdirectories of one repo share a clone directory, so they must
		// agree on the ref.
		if err := m.checkRefConflict(src, local); err != nil {
			return err
		}
		created, err := m.ensureClone(src, local)
		if err != nil {
			return err
		}
		// The subdirectory must actually be checked out, otherwise the
		// install silently contributes nothing.
		if err := checkInstallPath(src, m.installPath(src, local)); err != nil {
			if created {
				// Don't leave a stray clone behind for a bad subdirectory.
				_ = os.RemoveAll(m.gitInstallPath(src, local))
			}
			return err
		}
	} else {
		// Local: just verify the path exists.
		if _, err := os.Stat(src.Local); err != nil {
			return fmt.Errorf("local path %q does not exist: %w", src.Local, err)
		}
	}

	installPath := m.installPath(src, local)

	// Register in settings.
	if err := m.addPackage(source, local); err != nil {
		return err
	}

	// Print discovered resources.
	res, err := ScanPackageResources(installPath)
	if err != nil {
		return fmt.Errorf("scanning package at %s: %w", installPath, err)
	}
	fmt.Printf("Discovered: %d skill(s), %d extension(s), %d theme(s)\n",
		len(res.Skills), len(res.Extensions), len(res.Themes))
	if len(res.Skills)+len(res.Extensions)+len(res.Themes) == 0 {
		fmt.Printf("Warning: %s contains no skills, extensions, or themes at %s\n", source, installPath)
	}
	return nil
}

// checkRefConflict reports an error when an already-installed package shares
// src's clone directory but wants a different ref. One directory can only be
// checked out at one ref.
func (m *Manager) checkRefConflict(src *Source, projectScope bool) error {
	dest := m.gitInstallPath(src, projectScope)
	for _, other := range m.clonePeers(src, projectScope) {
		if other.Ref == src.Ref {
			continue
		}
		return fmt.Errorf("%s is already installed at ref %q and shares the clone at %s; "+
			"uninstall it first or use the same ref",
			other.Raw, refOrDefault(other.Ref), dest)
	}
	return nil
}

func refOrDefault(ref string) string {
	if ref == "" {
		return "default branch"
	}
	return ref
}

// ensureClone clones the repo for src, or refreshes an existing clone.
// Multiple subdirectories of the same repo share one clone directory, so an
// existing sparse clone must have the new subdirectory added to its sparse set.
// created reports whether this call made the clone.
func (m *Manager) ensureClone(src *Source, projectScope bool) (created bool, err error) {
	dest := m.gitInstallPath(src, projectScope)
	if _, statErr := os.Stat(dest); os.IsNotExist(statErr) {
		if src.SubDir != "" {
			fmt.Printf("Sparse-cloning %s (subdir: %s)...\n", src.URL, src.SubDir)
			return true, SparseCloneRef(src.URL, src.Ref, src.SubDir, dest)
		}
		fmt.Printf("Cloning %s...\n", src.URL)
		return true, CloneRef(src.URL, src.Ref, dest)
	}

	fmt.Printf("Already cloned at %s, updating...\n", dest)
	return false, m.refreshClone(dest, src, nil)
}

// refreshClone brings the clone at root up to date and makes src's files
// available in the working tree. Passing a non-nil pulled map deduplicates
// network round-trips across packages sharing one clone.
func (m *Manager) refreshClone(root string, src *Source, pulled map[string]bool) error {
	if !pulled[root] {
		if src.Pinned {
			// A pinned clone has a detached HEAD, where "pull --ff-only" fails.
			if err := Fetch(root); err != nil {
				return err
			}
			if err := CheckoutRef(root, src.Ref); err != nil {
				return err
			}
		} else if err := Pull(root); err != nil {
			return err
		}
		if pulled != nil {
			pulled[root] = true
		}
	}
	if src.SubDir != "" {
		// No-op on a full clone; idempotent on a sparse one.
		return SparseAdd(root, src.SubDir)
	}
	// Whole-repo package on top of a sparse clone: everything must be present.
	return SparseDisable(root)
}

// checkInstallPath returns a descriptive error when the package directory is
// missing after an install or update.
func checkInstallPath(src *Source, installPath string) error {
	if _, err := os.Stat(installPath); err != nil {
		if src.SubDir != "" {
			return fmt.Errorf("subdirectory %q not found in %s (checked %s): %w",
				src.SubDir, src.URL, installPath, err)
		}
		return fmt.Errorf("package directory %s is missing: %w", installPath, err)
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
		keep, wholeRepo := m.sharedClonePackages(src, local)
		switch {
		case wholeRepo:
			// A sibling package uses the whole repo — leave the clone alone.
			fmt.Printf("Keeping clone at %s (still used by another package)\n", dest)
		case len(keep) > 0:
			// Other subdirectories of the same repo are still installed:
			// shrink the sparse set instead of deleting the shared clone.
			fmt.Printf("Keeping clone at %s (still used by: %s)\n", dest, strings.Join(keep, ", "))
			if err := SparseSet(dest, keep); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(dest); err != nil {
				return fmt.Errorf("removing clone at %s: %w", dest, err)
			}
		}
	}

	fmt.Printf("Removed %s\n", source)
	return nil
}

// clonePeers returns the parsed sources of registered git packages in the
// given scope that share src's clone directory.
func (m *Manager) clonePeers(src *Source, projectScope bool) []*Source {
	dest := m.gitInstallPath(src, projectScope)
	entries := m.sm.GetGlobalPackages()
	if projectScope {
		entries = m.sm.GetProjectPackages()
	}
	var peers []*Source
	for _, e := range entries {
		raw := entrySource(e)
		if raw == "" {
			continue
		}
		other, err := ParseSource(raw)
		if err != nil || other.Type != "git" {
			continue
		}
		if m.gitInstallPath(other, projectScope) == dest {
			peers = append(peers, other)
		}
	}
	return peers
}

// sharedClonePackages returns the subdirectories of still-registered packages
// that share the clone directory of src in the same scope. wholeRepo is true
// when one of them is the repository root (no subdirectory).
func (m *Manager) sharedClonePackages(src *Source, projectScope bool) (subDirs []string, wholeRepo bool) {
	for _, other := range m.clonePeers(src, projectScope) {
		if other.SubDir == "" {
			wholeRepo = true
			continue
		}
		subDirs = append(subDirs, other.SubDir)
	}
	return subDirs, wholeRepo
}

// Update pulls the latest commits for one or all installed git packages.
// If source is "" all packages are updated.
func (m *Manager) Update(source string) error {
	pkgs, err := m.List()
	if err != nil {
		return err
	}
	var errs []error
	pulled := make(map[string]bool)
	wantID := ""
	if source != "" {
		src, err := ParseSource(source)
		if err != nil {
			return err
		}
		wantID = sourceIdentity(src)
	}
	for _, p := range pkgs {
		if wantID != "" && sourceIdentity(p.Source) != wantID {
			continue
		}
		if p.Source.Type != "git" {
			continue
		}
		fmt.Printf("Updating %s... ", p.Source.Raw)
		// Pull the clone root, not the subdirectory: several packages may
		// share one clone, and the subdirectory may not be checked out yet.
		root := m.gitInstallPath(p.Source, p.Scope == "project")
		if err := m.refreshClone(root, p.Source, pulled); err != nil {
			fmt.Printf("error: %v\n", err)
			errs = append(errs, err)
			continue
		}
		if err := checkInstallPath(p.Source, p.InstallPath); err != nil {
			fmt.Printf("error: %v\n", err)
			errs = append(errs, err)
			continue
		}
		fmt.Println("done")
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

// filterPackage returns pkgs without the entry matching source. Matching is by
// canonical identity so a different spelling of the same source still removes it.
func filterPackage(pkgs []any, source string) []any {
	targetID := ""
	if src, err := ParseSource(source); err == nil {
		targetID = sourceIdentity(src)
	}
	out := pkgs[:0:0]
	for _, p := range pkgs {
		raw := entrySource(p)
		if raw == source {
			continue
		}
		if targetID != "" {
			if src, err := ParseSource(raw); err == nil && sourceIdentity(src) == targetID {
				continue
			}
		}
		out = append(out, p)
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
// It returns the aggregate extension, skill, and theme paths
// from all installed packages so the resource loader can include them
// without importing pkg/pkg directly.
func (m *Manager) ResolvePackageResources() (extensions, skills, themes []string, err error) {
	rr, err := m.Resolve()
	if err != nil {
		return nil, nil, nil, err
	}
	return rr.Extensions, rr.Skills, rr.Themes, nil
}

// ResolvePackageContributions implements resources.ResourcePackageResolver.
// Returns one entry per installed package with its source string and
// per-resource-type paths, so loaders can attribute each resource back to
// the package that contributed it (origin = `pkg:<source>`).
func (m *Manager) ResolvePackageContributions() ([]resources.PackageContribution, error) {
	pkgs, err := m.List()
	if err != nil {
		return nil, err
	}
	out := make([]resources.PackageContribution, 0, len(pkgs))
	for _, p := range pkgs {
		c := resources.PackageContribution{
			Source:      p.Source.Raw,
			Scope:       p.Scope,
			InstallPath: p.InstallPath,
		}
		if p.Resources != nil {
			c.Extensions = append(c.Extensions, p.Resources.Extensions...)
			c.Skills = append(c.Skills, p.Resources.Skills...)
			c.Themes = append(c.Themes, p.Resources.Themes...)
		}
		out = append(out, c)
	}
	return out, nil
}
