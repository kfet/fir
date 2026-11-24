package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeAuto struct {
	now      time.Time
	exe      string
	writable bool
	fetched  *Release
	installs []string
	mod      time.Time
	failWith error
}

func (f *fakeAuto) env() autoEnv {
	return autoEnv{
		now:     func() time.Time { return f.now },
		exePath: func() (string, error) { return f.exe, nil },
		fetch: func(context.Context) (*Release, error) {
			if f.fetched == nil {
				return nil, errors.New("no fetch")
			}
			return f.fetched, nil
		},
		install: func(_ context.Context, rel *Release, _ string) error {
			if f.failWith != nil {
				return f.failWith
			}
			f.installs = append(f.installs, rel.Version)
			return nil
		},
		isWritable: func(string) bool { return f.writable },
		modTime:    func(string) (time.Time, error) { return f.mod, nil },
	}
}

func newFake() *fakeAuto {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	return &fakeAuto{now: now, exe: "/home/u/.local/bin/fir", writable: true}
}

func rel(v string, published time.Time) *Release {
	return &Release{Version: v, PublishedAt: published, inner: &asset{Name: "fir"}}
}

func TestAutoUpdate_InstallsAfterCooldown(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	got, err := autoUpdate(context.Background(), f.env(), "1.0.0", dir, rel("1.1.0", f.now.Add(-25*time.Hour)), DefaultAutoCooldown)
	if err != nil || got != "1.1.0" || len(f.installs) != 1 {
		t.Fatalf("got %q err %v installs %v", got, err, f.installs)
	}
	// The old process restarted later still reports the pending install.
	got, _ = autoUpdate(context.Background(), f.env(), "1.0.0", dir, nil, DefaultAutoCooldown)
	if got != "1.1.0" {
		t.Fatalf("pending = %q", got)
	}
	// And does not install it twice.
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, rel("1.1.0", f.now.Add(-25*time.Hour)), DefaultAutoCooldown)
	if len(f.installs) != 1 {
		t.Fatalf("installed twice: %v", f.installs)
	}
	// A manual reinstall since voids the pending install.
	f.mod = f.mod.Add(time.Second)
	if got, _ := autoUpdate(context.Background(), f.env(), "1.0.0", dir, nil, DefaultAutoCooldown); got != "" {
		t.Fatalf("stale pending = %q", got)
	}
	// The new binary sees nothing pending.
	if got, _ := autoUpdate(context.Background(), f.env(), "1.1.0", dir, nil, DefaultAutoCooldown); got != "" {
		t.Fatalf("new binary pending = %q", got)
	}
}

func TestAutoUpdate_WaitsForCooldown(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	got, _ := autoUpdate(context.Background(), f.env(), "1.0.0", dir, rel("1.1.0", f.now.Add(-time.Hour)), DefaultAutoCooldown)
	if got != "" || len(f.installs) != 0 {
		t.Fatalf("installed too early: %q %v", got, f.installs)
	}
}

func TestAutoUpdate_FirstSeenFallback(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	r := rel("1.1.0", time.Time{})
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 0 {
		t.Fatal("installed on first sight")
	}
	f.now = f.now.Add(DefaultAutoCooldown)
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 1 {
		t.Fatal("did not install after first-seen cool-down")
	}
}

func TestAutoUpdate_SkipsDevBrewAndReadOnly(t *testing.T) {
	old := rel("1.1.0", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	for name, tc := range map[string]struct {
		version string
		mod     func(*fakeAuto)
	}{
		"dev":      {"dev", func(*fakeAuto) {}},
		"devbuild": {"1.0.0-dev+abc", func(*fakeAuto) {}},
		"brew":     {"1.0.0", func(f *fakeAuto) { f.exe = "/opt/homebrew/Cellar/fir/1.0.0/bin/fir" }},
		"readonly": {"1.0.0", func(f *fakeAuto) { f.writable = false }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			tc.mod(f)
			got, err := autoUpdate(context.Background(), f.env(), tc.version, t.TempDir(), old, DefaultAutoCooldown)
			if got != "" || err != nil || len(f.installs) != 0 {
				t.Fatalf("got %q err %v installs %v", got, err, f.installs)
			}
		})
	}
}

func TestAutoUpdate_BacksOffAfterFailure(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.failWith = errors.New("boom")
	r := rel("1.1.0", f.now.Add(-48*time.Hour))
	if _, err := autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown); err == nil {
		t.Fatal("want error")
	}
	f.failWith = nil
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 0 {
		t.Fatal("retried inside back-off")
	}
	f.now = f.now.Add(autoRetryAfter)
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 1 {
		t.Fatal("did not retry after back-off")
	}
}

func TestAutoUpdate_CachedReleaseRefetches(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	published := f.now.Add(-48 * time.Hour)
	cached := &Release{Version: "1.1.0", PublishedAt: published}
	// A newer release appeared since the check: not cooled down, skip.
	f.fetched = rel("1.2.0", f.now)
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, cached, DefaultAutoCooldown)
	if len(f.installs) != 0 {
		t.Fatalf("installed uncooled release: %v", f.installs)
	}
	f.fetched = rel("1.1.0", published)
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, cached, DefaultAutoCooldown)
	if len(f.installs) != 1 || f.installs[0] != "1.1.0" {
		t.Fatalf("installs %v", f.installs)
	}
}

func TestAutoUpdate_LockHeld(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	unlock, ok := acquireAutoLock(filepath.Join(dir, autoLockName))
	if !ok {
		t.Fatal("lock")
	}
	r := rel("1.1.0", f.now.Add(-48*time.Hour))
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 0 {
		t.Fatal("installed while locked")
	}
	unlock()
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, r, DefaultAutoCooldown)
	if len(f.installs) != 1 {
		t.Fatal("not installed after unlock")
	}
}

func TestAutoUpdate_PendingNeedsSameExe(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, rel("1.1.0", f.now.Add(-48*time.Hour)), DefaultAutoCooldown)
	f.exe = "/elsewhere/fir"
	if got, _ := autoUpdate(context.Background(), f.env(), "1.0.0", dir, nil, DefaultAutoCooldown); got != "" {
		t.Fatalf("pending for another exe = %q", got)
	}
}

func TestAutoUpdate_LockDirMissing(t *testing.T) {
	f := newFake()
	dir := filepath.Join(t.TempDir(), "missing")
	autoUpdate(context.Background(), f.env(), "1.0.0", dir, rel("1.1.0", f.now.Add(-48*time.Hour)), DefaultAutoCooldown)
	if len(f.installs) != 0 {
		t.Fatal("installed without a lock")
	}
}

func TestNoticeText(t *testing.T) {
	if (Notice{}).Text() != "" {
		t.Fatal("empty notice")
	}
	if (Notice{Version: "1.2.0"}).Text() != UpdateNotice("1.2.0") {
		t.Fatal("available")
	}
	if (Notice{Version: "1.2.0", Installed: true}).Text() != InstalledNotice("1.2.0") {
		t.Fatal("installed")
	}
}

func TestPickReleaseCarriesPublishedAt(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rels := []ghRelease{{Tag: "v1.2.3", PublishedAt: when, Assets: []asset{{Name: "fir_linux_amd64"}}}}
	a, _, ok := pickRelease(rels, "linux", "amd64", 0)
	if !ok || !a.published.Equal(when) {
		t.Fatalf("published = %v", a)
	}
}

func TestAutoUpdate_PublicSkipsDev(t *testing.T) {
	if got, err := AutoUpdate(context.Background(), "dev", t.TempDir(), rel("9.9.9", time.Time{}), 0); got != "" || err != nil {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestDefaultAutoEnv(t *testing.T) {
	env := defaultAutoEnv()
	dir := t.TempDir()
	if !env.isWritable(dir) || env.isWritable(filepath.Join(dir, "missing")) {
		t.Fatal("isWritable")
	}
	if _, err := env.modTime(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := env.modTime(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("modTime of missing file")
	}
	if err := env.install(context.Background(), &Release{Version: "1.0.0"}, filepath.Join(dir, "fir")); err == nil {
		t.Fatal("install without assets")
	}
}
