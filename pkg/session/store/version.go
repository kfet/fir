package store

import (
	"runtime/debug"
	"sync"
)

// firVersion holds the fir binary version recorded on session header records.
// It is set once at process startup via SetFirVersion (mirroring the
// interactive/acp SetVersion pattern) and defaults to "dev" for builds and
// tests that never call it. Guarded by versionMu so a late startup write does
// not race header creation on another goroutine.
var (
	versionMu  sync.RWMutex
	firVersion = "dev"
)

// SetFirVersion records the fir binary version stamped onto new session header
// records. Call once from the CLI entrypoint with the ldflags-injected version.
func SetFirVersion(v string) {
	if v == "" {
		return
	}
	versionMu.Lock()
	firVersion = v
	versionMu.Unlock()
}

// currentFirVersion returns the version stamped onto new session headers.
func currentFirVersion() string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return firVersion
}

// firCommit is the VCS revision embedded by the Go toolchain at build time,
// computed once. Empty when the build carried no VCS stamp (e.g. `go test`,
// or builds outside a git checkout) — in that case the header omits the field.
var firCommit = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
})
