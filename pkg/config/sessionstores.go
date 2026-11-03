package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SessionStoresEnv names the environment variable that replaces the
// sessionStores setting for one invocation: "label=path[,label=path]".
const SessionStoresEnv = "FIR_SESSION_STORES"

// DefaultStoreLabel is the label of fir's own (writable) session store. It is
// reserved: no extra store may use it.
const DefaultStoreLabel = "local"

// SessionStoreSettings is one entry of the sessionStores setting: an extra,
// read-only directory laid out like ~/.config/fir/sessions (one
// --<encoded-cwd>-- subdirectory per project), e.g. a synced archive of
// another host's sessions.
type SessionStoreSettings struct {
	Path  string `json:"path"`
	Label string `json:"label,omitempty"`
	// ReadOnly must be true or omitted. Extra stores are always read-only;
	// false is ignored with a warning.
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// SessionStore is a validated extra session store.
type SessionStore struct {
	Label string
	Path  string // absolute, cleaned, ~ expanded
}

var storeLabelRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ExpandHome expands a leading "~" or "~/" to the user's home directory.
// Other paths are returned unchanged.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

func expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = ExpandHome(p)
	}
	return out
}

// ParseSessionStoresEnv parses the FIR_SESSION_STORES syntax
// "label=path[,label=path]". An entry without "=" is a bare path whose label
// defaults to the directory's base name.
func ParseSessionStoresEnv(v string) []SessionStoreSettings {
	var out []SessionStoreSettings
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, path, ok := strings.Cut(part, "=")
		if !ok {
			label, path = "", part
		}
		out = append(out, SessionStoreSettings{Label: strings.TrimSpace(label), Path: strings.TrimSpace(path)})
	}
	return out
}

// ResolveSessionStores validates raw sessionStores entries: expands ~,
// requires an absolute path, defaults the label to the path's base name, and
// drops entries with a bad, reserved or duplicate label or a duplicate path.
// Missing directories are kept (a synced mount may appear later); listing
// skips them. Problems are returned as warnings, never as a hard error, so
// a bad entry never stops fir from starting.
func ResolveSessionStores(raw []SessionStoreSettings) ([]SessionStore, []string) {
	var (
		out      []SessionStore
		warnings []string
		labels   = map[string]bool{DefaultStoreLabel: true}
		paths    = map[string]bool{}
	)
	for _, r := range raw {
		p := ExpandHome(strings.TrimSpace(r.Path))
		if p == "" || !filepath.IsAbs(p) {
			warnings = append(warnings, fmt.Sprintf("sessionStores: path %q must be absolute (or start with ~/); skipped", r.Path))
			continue
		}
		p = filepath.Clean(p)
		label := strings.TrimSpace(r.Label)
		if label == "" {
			label = filepath.Base(p)
		}
		if !storeLabelRE.MatchString(label) {
			warnings = append(warnings, fmt.Sprintf("sessionStores: label %q must match [A-Za-z0-9._-]+; %s skipped", label, p))
			continue
		}
		if labels[label] {
			warnings = append(warnings, fmt.Sprintf("sessionStores: label %q is reserved or already used; %s skipped", label, p))
			continue
		}
		if paths[p] {
			warnings = append(warnings, fmt.Sprintf("sessionStores: path %s listed twice; %q skipped", p, label))
			continue
		}
		if r.ReadOnly != nil && !*r.ReadOnly {
			warnings = append(warnings, fmt.Sprintf("sessionStores: %q: readOnly:false is not supported — extra stores are always read-only", label))
		}
		labels[label] = true
		paths[p] = true
		out = append(out, SessionStore{Label: label, Path: p})
	}
	return out, warnings
}

// LoadSessionStores returns the extra session stores for this process:
// FIR_SESSION_STORES when set (even to ""), else the sessionStores array of
// the GLOBAL settings file (<agentDir>/settings.json). A project's
// .fir/settings.json cannot add stores — a repository must not be able to
// widen what an ACP client may resume — so a sessionStores key there only
// produces a warning.
func LoadSessionStores(cwd, agentDir string) ([]SessionStore, []string) {
	var warnings []string
	storage := NewFileSettingsStorage(cwd, agentDir)
	if proj, err := loadSettingsFromStorage(storage, ScopeProject); err == nil && proj.SessionStores != nil {
		warnings = append(warnings, fmt.Sprintf("sessionStores in %s ignored: it is a global-only setting (%s)",
			storage.PathForScope(ScopeProject), storage.PathForScope(ScopeGlobal)))
	}
	var raw []SessionStoreSettings
	if v, ok := os.LookupEnv(SessionStoresEnv); ok {
		raw = ParseSessionStoresEnv(v)
	} else if global, err := loadSettingsFromStorage(storage, ScopeGlobal); err == nil {
		raw = global.SessionStores
	}
	stores, w := ResolveSessionStores(raw)
	return stores, append(warnings, w...)
}
