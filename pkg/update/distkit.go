// distkit.go wires github.com/kfet/distkit into fir as a DORMANT second
// update path.
//
// distkit is the family's single home for binary distribution and self-update;
// six sibling repos already use it, and fir/pkg/update/brew.go is in fact where
// distkit's brew support was lifted from. fir is the last holdout, and it is
// also the tool you need in working order to recover from a bad update — a
// bricked relay is an inconvenience, a bricked fir on a fleet host is an
// SSH-and-curl afternoon.
//
// So this release is a BRIDGE, not a swap:
//
//   - distkit RESOLVES and REPORTS: it backs `fir update -check` and runs
//     alongside go-selfupdate in the cached background check, where any
//     disagreement between the two resolvers is logged.
//   - go-selfupdate still performs every byte of the actual swap (see
//     SelfUpdate in update.go).
//
// Nothing in this file may call distkit.Update, distkit.Download,
// distkit.Apply, distkit.Main or distkit.StagingDir: those write a binary.
// TestDistkitPathCannotSwap in distkit_dormant_test.go scans the source tree
// and fails if any of them appears anywhere in fir.
//
// The swap moves to distkit in a later release, once the fleet has produced
// agreement data from the logging below.
package update

import (
	"context"
	"fmt"
	"time"

	"github.com/kfet/distkit"

	"github.com/kfet/fir/pkg/log"
)

// DistkitConfig builds the distkit configuration for fir's read-only paths.
//
// The only subtlety is the repo: fir's SOURCE is kfet/fir, but its release
// ASSETS live in the separate public mirror kfet/fir-dist, which is the stable
// distribution endpoint baked into every binary ever shipped (see the comment
// on repoName in update.go). distkit does not care where the source lives —
// this is one config string.
//
// Every naming-relevant field is set explicitly rather than left to distkit's
// internal defaults, because fir renders asset names (in the parity test)
// without going through distkit's unexported normalisation.
func DistkitConfig(currentVersion string) distkit.Config {
	cfg := distkit.Config{
		Repo:      repoOwner + "/" + repoName,
		Binary:    "fir",
		AssetStem: "fir",
		Version:   currentVersion,
		// goreleaser publishes raw binaries named "fir-{os}-{arch}",
		// 32-bit ARM as "armv6" (goarm: "6"). See .goreleaser.yaml.
		AssetTemplate:  distkit.DefaultAssetTemplate,
		ChecksumsAsset: distkit.DefaultChecksums,
		ArmSuffix:      "armv6",
	}
	// Test seam: empty in production, so the real hosts are used.
	cfg.APIBase, cfg.DownloadBase = distkitAPIBase, distkitDownloadBase
	return cfg
}

// distkitAPIBase and distkitDownloadBase are empty in production — distkit
// then uses github.com and api.github.com. Tests point them at an httptest
// server so the resolver can be exercised without the network.
var distkitAPIBase, distkitDownloadBase string

// shadowResolve carries the dormant resolver's answer back to the cached
// check that started it.
type shadowResolve struct {
	ch <-chan shadowOutcome
}

type shadowOutcome struct {
	version string
	err     error
}

// startShadowResolve kicks off the dormant distkit resolve so it runs
// alongside the authoritative go-selfupdate one. The channel is buffered, so
// a caller that gives up (because the authoritative check failed) leaks
// nothing: the goroutine sends and exits regardless.
func startShadowResolve(ctx context.Context, currentVersion string) shadowResolve {
	ch := make(chan shadowOutcome, 1)
	go func() {
		v, err := DistkitResolve(ctx, currentVersion, true /* anonymous */)
		ch <- shadowOutcome{version: v, err: err}
	}()
	return shadowResolve{ch: ch}
}

// result waits for the shadow resolver and compares it with the authoritative
// answer, returning what should be persisted in the cache entry.
//
// Visibility is deliberately two-tier. A disagreement is logged at WARN —
// loud enough that anyone who looks at a log sees it, quiet enough that it
// cannot become noise, since the whole thing runs at most once per 24h behind
// the cache. Agreement is logged at DEBUG. Both outcomes are also persisted
// in update-check.json, so the fleet can be swept for disagreement after the
// fact without debug logging having been enabled at the right moment.
func (s shadowResolve) result(authoritative string) (version string, errText string) {
	out := <-s.ch
	if out.err != nil {
		// Not a disagreement — the dormant path could not answer at all.
		// Still WARN: a resolver that cannot resolve is exactly what this
		// release exists to find out about before the swap moves over.
		log.Warn("distkit shadow update check failed",
			"error", out.err, "go_selfupdate", authoritative)
		return "", out.err.Error()
	}
	if distkit.EnsureV(out.version) != distkit.EnsureV(authoritative) {
		log.Warn("update resolvers disagree — distkit is dormant, go-selfupdate decided",
			"distkit", out.version, "go_selfupdate", authoritative)
		return out.version, ""
	}
	log.Debug("update resolvers agree", "version", out.version)
	return out.version, ""
}

// recordAsync joins the shadow resolver off the caller's critical path and
// rewrites the cache entry with what it found. The authoritative answer has
// already been cached and returned by then, so a dormant resolver that hangs
// until the context deadline costs the user nothing.
//
// The returned channel is closed once the record is written. Production
// ignores it — if the process exits first the next check simply asks again —
// and tests join on it instead of sleeping.
func (s shadowResolve) recordAsync(cachePath, authoritative string, checkedAt time.Time) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		version, errText := s.result(authoritative)
		writeCache(cachePath, &cacheEntry{
			CheckedAt:      checkedAt,
			LatestVersion:  authoritative,
			DistkitVersion: version,
			DistkitError:   errText,
		})
	}()
	return done
}

// LastDisagreement returns a human-readable line when the most recent cached
// background check found the two resolvers disagreeing, and "" otherwise.
//
// liveOK says the caller has just resolved through distkit successfully, in
// which case a cached FAILURE is stale by definition — the cache is rewritten
// at most once per 24h, so one GitHub blip would otherwise have a host
// reporting a failed dormant check for a day after it started working again.
// A cached disagreement is still reported: that one is about the answer, not
// about reachability, and the live check does not refute it.
//
// It reads the cache only — no network — so a command can surface the state
// cheaply. cacheDir is the agent directory.
func LastDisagreement(cacheDir string, liveOK bool) string {
	entry, ok := readCache(cacheDir + "/update-check.json")
	if !ok {
		return ""
	}
	switch {
	case entry.DistkitError != "":
		if liveOK {
			return ""
		}
		return fmt.Sprintf("note: the dormant distkit update check failed at %s: %s",
			entry.CheckedAt.Format(time.RFC3339), entry.DistkitError)
	case entry.DistkitVersion == "" || entry.LatestVersion == "":
		return ""
	case distkit.EnsureV(entry.DistkitVersion) != distkit.EnsureV(entry.LatestVersion):
		return fmt.Sprintf("note: update resolvers disagreed at %s: distkit says %s, go-selfupdate says %s (distkit is dormant; go-selfupdate decides)",
			entry.CheckedAt.Format(time.RFC3339), entry.DistkitVersion, entry.LatestVersion)
	}
	return ""
}

// CheckReport is what a report-only check resolved. It is deliberately not a
// Release: nothing downstream of it can download or install anything.
type CheckReport struct {
	// Current is the running version, with a leading "v".
	Current string
	// Target is the tag distkit resolved as latest, with a leading "v".
	Target string
	// Available reports whether Target is strictly newer than Current, by
	// fir's IsNewer. Always false for a Dev build: it sits after its tag.
	Available bool
	// Brew names the Homebrew formula when this install is a keg, in which
	// case an update comes from `brew upgrade`, not from a self-update.
	Brew string
	// Dev marks a working-tree build (e.g. "dev", "1.7.1-dev+abc"). Such a
	// build sits AFTER its tag, so there is nothing to update to and the
	// report says so rather than claiming to be up to date with a release
	// it is not running.
	Dev bool
}

// DistkitCheck resolves the latest release through distkit and reports it.
// It is the engine behind `fir update -check`.
//
// It calls distkit.Check — never distkit.Update, even with CheckOnly set:
// this release's dormancy guarantee is worth more than the few lines of
// printing that would save, and one boolean is too small a gap between a
// report and a binary swap. See TestDistkitPathCannotSwap.
func DistkitCheck(ctx context.Context, currentVersion string) (*CheckReport, error) {
	rep := &CheckReport{Current: distkit.EnsureV(currentVersion), Dev: distkit.IsDevBuild(currentVersion)}
	if rep.Dev {
		// "vdev" is not a version. Report what the binary actually says.
		rep.Current = currentVersion
	}

	// Best-effort: a keg install changes the advice, not the resolution, so
	// a detection failure must not fail the check.
	if inst, err := DetectBrewInstall(ctx); err == nil && inst != nil {
		rep.Brew = inst.Formula
	}

	target, err := DistkitResolve(ctx, currentVersion, false)
	if err != nil {
		return nil, err
	}
	rep.Target = target
	// One predicate decides: a working-tree build sits AFTER its tag, so
	// there is nothing to update to and the report says exactly that rather
	// than offering to install the release it was built past. Everything
	// else is fir's own IsNewer, the same comparison the background notice
	// uses.
	rep.Available = !rep.Dev && IsNewer(target, currentVersion)
	return rep, nil
}

// DistkitResolve asks distkit which release `fir update` would install, and
// returns its tag (with a leading "v"). It reads only: no download, no swap.
//
// anonymous=true forbids distkit from authenticating at all. The background
// check sets it for two reasons: token discovery execs `gh auth token` (up to
// 10s, outside our context) on a startup path, and a resolver whose path
// depends on whether the host happens to have `gh` logged in produces
// disagreement data nobody can interpret. go-selfupdate resolves the
// background check anonymously too, so this is the only like-for-like
// comparison. Spending no API quota is the bonus: the anonymous resolve goes
// through the /releases/latest redirect on the download host, which has no
// per-IP budget, unlike the 60/hour API cap a NAT'd fleet shares.
func DistkitResolve(ctx context.Context, currentVersion string, anonymous bool) (string, error) {
	cfg := DistkitConfig(currentVersion)
	cfg.Anonymous = anonymous
	st, err := distkit.Check(ctx, cfg)
	if err != nil {
		return "", err
	}
	return st.Target, nil
}
