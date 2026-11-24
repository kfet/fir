package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	goversion "github.com/hashicorp/go-version"
)

// DefaultAutoCooldown is how old a release must be before fir installs it on
// its own. A day lets a bad release be caught and superseded before it lands
// everywhere.
const DefaultAutoCooldown = 24 * time.Hour

// EnvNoAutoUpdate disables auto-update when set to any non-empty value.
const EnvNoAutoUpdate = "FIR_NO_AUTO_UPDATE"

const (
	autoStateName = "auto-update.json"
	autoLockName  = "auto-update.lock"
)

var (
	// autoRetryAfter backs off after a failed install of the same version.
	autoRetryAfter = 6 * time.Hour
	// errNotAutoUpdatable marks installs fir must not rewrite on its own.
	errNotAutoUpdatable = errors.New("install is not auto-updatable")
)

// autoState is persisted to agentDir/auto-update.json.
type autoState struct {
	// InstalledVersion is the last version auto-update put on disk.
	InstalledVersion string    `json:"installed_version,omitempty"`
	InstalledAt      time.Time `json:"installed_at,omitempty"`
	// InstalledExe/InstalledMod identify the file written, so a manual
	// reinstall or downgrade since then voids InstalledVersion.
	InstalledExe string    `json:"installed_exe,omitempty"`
	InstalledMod time.Time `json:"installed_mod,omitempty"`
	// SeenVersion/SeenAt start the cool-down when the publish time is unknown.
	SeenVersion string    `json:"seen_version,omitempty"`
	SeenAt      time.Time `json:"seen_at,omitempty"`
	// FailedVersion/FailedAt/Error back off after a failed install.
	FailedVersion string    `json:"failed_version,omitempty"`
	FailedAt      time.Time `json:"failed_at,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// autoEnv holds the side effects AutoUpdate needs, so tests can replace them.
type autoEnv struct {
	now        func() time.Time
	exePath    func() (string, error)
	fetch      func(ctx context.Context) (*Release, error)
	install    func(ctx context.Context, rel *Release, exe string) error
	isWritable func(dir string) bool
	modTime    func(path string) (time.Time, error)
}

func defaultAutoEnv() autoEnv {
	return autoEnv{
		now:     time.Now,
		exePath: executablePath,
		fetch:   FetchLatest,
		install: func(ctx context.Context, rel *Release, exe string) error {
			if rel.inner == nil {
				return fmt.Errorf("no release assets available")
			}
			return updateTo(ctx, rel.inner, exe)
		},
		isWritable: dirWritable,
		modTime: func(path string) (time.Time, error) {
			info, err := os.Stat(path)
			if err != nil {
				return time.Time{}, err
			}
			return info.ModTime(), nil
		},
	}
}

// AutoUpdate installs latest over the running binary once the release is at
// least cooldown old. It never restarts fir: the new binary is used on the
// next start.
//
// latest is what CheckLatest returned (nil when up to date). It returns the
// version now on disk that is newer than currentVersion — installed by this
// call or by an earlier one — or "" when there is none. Dev builds, brew
// installs and binaries in a directory fir cannot write are left alone.
func AutoUpdate(ctx context.Context, currentVersion, stateDir string, latest *Release, cooldown time.Duration) (string, error) {
	return autoUpdate(ctx, defaultAutoEnv(), currentVersion, stateDir, latest, cooldown)
}

func autoUpdate(ctx context.Context, env autoEnv, currentVersion, stateDir string, latest *Release, cooldown time.Duration) (string, error) {
	if !autoUpdatableVersion(currentVersion) {
		return "", nil
	}
	statePath := filepath.Join(stateDir, autoStateName)
	st := readAutoState(statePath)
	pending := pendingVersion(env, st, currentVersion)
	if latest == nil {
		return pending, nil
	}
	base := currentVersion
	if pending != "" {
		base = pending
	}
	if !IsNewer(latest.Version, base) {
		return pending, nil
	}

	now := env.now()
	if !cooledDown(statePath, &st, latest, now, cooldown) {
		return pending, nil
	}
	if st.FailedVersion == latest.Version && now.Sub(st.FailedAt) < autoRetryAfter {
		return pending, nil
	}

	unlock, ok := acquireAutoLock(filepath.Join(stateDir, autoLockName))
	if !ok {
		return pending, nil // another fir process is on it
	}
	defer unlock()

	// Another process may have installed while we waited for the lock.
	st = readAutoState(statePath)
	if pending = pendingVersion(env, st, currentVersion); pending != "" && !IsNewer(latest.Version, pending) {
		return pending, nil
	}

	exe, err := installRelease(ctx, env, latest)
	if errors.Is(err, errNotAutoUpdatable) {
		return pending, nil
	}
	if err != nil {
		st.FailedVersion, st.FailedAt, st.Error = latest.Version, now, err.Error()
		writeAutoState(statePath, st)
		return pending, err
	}
	mod, _ := env.modTime(exe)
	st.InstalledVersion, st.InstalledAt = latest.Version, now
	st.InstalledExe, st.InstalledMod = exe, mod
	st.FailedVersion, st.FailedAt, st.Error = "", time.Time{}, ""
	writeAutoState(statePath, st)
	return latest.Version, nil
}

// pendingVersion returns the version auto-update installed when it is newer
// than the running one and still the file on disk.
func pendingVersion(env autoEnv, st autoState, currentVersion string) string {
	if st.InstalledVersion == "" || !IsNewer(st.InstalledVersion, currentVersion) {
		return ""
	}
	exe, err := env.exePath()
	if err != nil || exe != st.InstalledExe {
		return ""
	}
	if mod, err := env.modTime(exe); err != nil || !mod.Equal(st.InstalledMod) {
		return ""
	}
	return st.InstalledVersion
}

// cooledDown reports whether latest is old enough to install. It counts from
// the publish time, or from when this host first saw the version when the
// publish time is unknown (updating st for the caller to persist).
func cooledDown(statePath string, st *autoState, latest *Release, now time.Time, cooldown time.Duration) bool {
	start := latest.PublishedAt
	if start.IsZero() {
		if st.SeenVersion != latest.Version {
			// Re-read so a concurrent install is not overwritten.
			*st = readAutoState(statePath)
			if st.SeenVersion != latest.Version {
				st.SeenVersion, st.SeenAt = latest.Version, now
				writeAutoState(statePath, *st)
			}
		}
		start = st.SeenAt
	}
	return now.Sub(start) >= cooldown
}

func installRelease(ctx context.Context, env autoEnv, latest *Release) (string, error) {
	exe, err := env.exePath()
	if err != nil {
		return "", fmt.Errorf("locate current executable: %w", err)
	}
	if _, brew := splitCellarPath(exe); brew {
		return "", errNotAutoUpdatable // brew owns this binary
	}
	if !env.isWritable(filepath.Dir(exe)) {
		return "", errNotAutoUpdatable // e.g. root-owned system packaging
	}
	rel := latest
	if rel.inner == nil {
		if rel, err = env.fetch(ctx); err != nil {
			return "", err
		}
		// A newer release appeared since the check: it has its own
		// cool-down, which the next check starts.
		if rel.Version != latest.Version {
			return "", errNotAutoUpdatable
		}
	}
	return exe, env.install(ctx, rel, exe)
}

// autoUpdatableVersion rejects dev and source builds: a release must never
// overwrite a binary a developer built by hand.
func autoUpdatableVersion(v string) bool {
	if v == "" || v == "dev" {
		return false
	}
	gv, err := goversion.NewVersion(v)
	return err == nil && gv.Prerelease() == ""
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".fir-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// acquireAutoLock takes an exclusive, non-blocking flock. The kernel drops
// it when the holder dies, so no stale-lock logic is needed.
func acquireAutoLock(path string) (func(), bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}

func readAutoState(path string) autoState {
	var st autoState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func writeAutoState(path string, st autoState) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auto-update-*.json")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

// InstalledNotice is the one-line message shown once a newer fir is on disk.
func InstalledNotice(newVersion string) string {
	return fmt.Sprintf("› fir %s installed — restart fir to use it", newVersion)
}

// Notice is the result of the startup update check.
type Notice struct {
	// Version is the newer release, or "" when there is nothing to report.
	Version string
	// Installed is true when Version is already on disk and only a restart
	// is needed.
	Installed bool
}

// Text renders the notice as one line ("" when there is nothing to say).
func (n Notice) Text() string {
	switch {
	case n.Version == "":
		return ""
	case n.Installed:
		return InstalledNotice(n.Version)
	default:
		return UpdateNotice(n.Version)
	}
}
