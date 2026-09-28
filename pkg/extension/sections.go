package extension

import (
	"github.com/kfet/fir/pkg/sections"
)

// SectionPolicy decides which on-disk sections fir may emit. It is computed
// from discovery alone — no extension process is consulted.
type SectionPolicy struct {
	// Eligible holds owners that are installed, enabled for this session,
	// and trusted (builtin/global/package, or a trusted project extension).
	Eligible map[string]bool
	// Installed holds every discovered owner, enabled or not.
	Installed map[string]bool
}

// Allowed reports whether a section owned by name may be emitted.
func (p SectionPolicy) Allowed(name string) bool { return p.Eligible[name] }

// SetSectionStore sets the store set_section writes to. Call before Start.
func (m *Manager) SetSectionStore(store *sections.Store) {
	m.mu.Lock()
	m.sectionStore = store
	m.mu.Unlock()
}

// SectionPolicy computes the section policy for projectDir.
func (m *Manager) SectionPolicy(projectDir string) (SectionPolicy, error) {
	pol := SectionPolicy{Eligible: map[string]bool{}, Installed: map[string]bool{}}
	configs, err := m.discoverAll(projectDir)
	if err != nil {
		return pol, err
	}
	for _, cfg := range configs {
		pol.Installed[cfg.Name] = true
		if m.shouldSkip(cfg) || reservedSourceName(cfg.Name) {
			continue
		}
		if cfg.Scope == "project" {
			hash, err := ComputeHash(cfg.Path)
			if err != nil || !m.trust.IsTrusted(projectDir, cfg.Name, hash) {
				continue
			}
		}
		pol.Eligible[cfg.Name] = true
	}
	return pol, nil
}

// PruneSections deletes sections whose owner is uninstalled: not discovered
// here and not a trusted project extension of some other project. Disabled
// owners keep their file.
func (m *Manager) PruneSections(store *sections.Store, pol SectionPolicy) {
	all, err := store.ReadAll()
	if err != nil {
		return
	}
	for _, s := range all {
		if pol.Installed[s.Name] || m.trust.KnowsName(s.Name) {
			continue
		}
		if err := store.Clear(s.Name); err != nil {
			m.logger.Warn("pruning orphaned section failed", "section", s.Name, "err", err)
		} else {
			m.logger.Info("pruned section of uninstalled extension", "section", s.Name)
		}
	}
}
