package extension

import (
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/resources"
)

// ExtraSources bundles the additional, lower-priority extension sources that
// live outside the project/global .fir/extensions directories: individual
// script files contributed by installed fir packages (Files) and extra scan
// directories from the settings `extensionPaths` key (Dirs). Both are shadowed
// by project/global extensions of the same name (see mergeConfigsByName).
//
// It is embedded in both SetupOptions and AuthSetupOptions so that every mode
// (CLI/TUI, ACP session setup, ACP auth-provider bootstrap) carries the same
// shape. Adding a new source kind here — and teaching ResolveExtraSources to
// populate it — reaches every mode by type, instead of relying on each call
// site to remember a struct literal.
type ExtraSources struct {
	// Files lists individual extension script paths (e.g. package-contributed).
	// Each is treated as a "package"-scoped extension.
	Files []string
	// Dirs lists additional directories to scan for extension scripts. Each is
	// scanned with "package" scope.
	Dirs []string
}

// ResolveExtraSources builds the ExtraSources for a mode: the package-provided
// extension files supplied by the caller, plus the settings `extensionPaths`
// directories resolved against cwd. This is the single shared constructor used
// by all four call sites (cmd/fir app + login, ACP session setup, ACP auth
// bootstrap) so the wiring cannot drift between modes.
//
// Lives in pkg/extension (which already imports pkg/resources) rather than
// pkg/resources: pkg/resources does not import pkg/extension, so keeping the
// constructor here avoids an import cycle.
func ResolveExtraSources(cwd string, sm *config.SettingsManager, pkgFiles []string) ExtraSources {
	return ExtraSources{
		Files: pkgFiles,
		Dirs:  resources.ResolveSettingsExtensionPaths(cwd, sm),
	}
}
