# Coverage-gate on-ramp: queue items 1-6 sealed

Branch: `work/pkg-coverage` (worktree `~/src/fir-covpkg`), rebased onto local
`main` at **v1.11.0** (`f34725fd`). Not merged, not pushed. `make all` is green
on that base.

Every figure below was re-measured on the post-rebase tree with
`make coverage`. The previous revision of this file carried numbers taken
against v1.1.1; they have all moved and none of them is reproduced here.

## Result

The whole on-ramp from `BACKLOG.md` is cleared, in queue order, one commit per
package. Nothing was added to `.covignore` to achieve it.

| # | Package | Before (main) | After | Gated statements |
| ---: | --- | ---: | ---: | ---: |
| 1 | `pkg/extension/apikind` | 0.0% | **100.0%** | 6 |
| 2 | `pkg/ai/envkeys` | 61.7% | **100.0%** | 47 |
| 3 | `pkg/extension/sdk` | 71.6% | **100.0%** | 66 |
| 4 | `pkg/ai/providers/declcfg` | 82.8% | **100.0%** | 121 |
| 5 | `pkg/agent/tools` | 77.8% | **100.0%** | 117 |
| 6 | `pkg/log` | 77.9% | **100.0%** | 199 |
| 12 | `pkg/pkg` (done before the retarget) | 69.6% | **100.0%** | 518 |

"Before" is `go test -covermode=set ./...` on `f34725fd` with none of this
branch applied. Statement counts are today's, from `bin/coverage.gated.out`,
and differ from the pre-rebase report where main's own commits moved them
(`sdk` 64 → 66 via the cache refactor, `declcfg` 122 → 121, `pkg/pkg`
520 → 518 after this branch's own dead-code deletion).

| Metric | At adoption | Now (measured) |
| --- | ---: | ---: |
| Gated at a hard `-min=100` | 372 statements (1.2%) | **1,483 (4.5%)** |
| Excluded — structural | 12,446 (39.5%) | 13,078 (40.1%) |
| Excluded — pure debt | 18,677 (59.3%) | **18,033 (55.3%)** |
| Whole tree (`COVERAGE_FLOOR` tier) | 67.0% | **68.7%** |
| `COVERAGE_FLOOR` | 66 | **67** |
| Tree size | 31,495 statements | 32,594 statements |

The structural bucket grew without a line being added to section 1 — the tree
itself grew ~1,100 statements over the 98 commits this branch was rebased
across, and section 1's patterns caught their share of it.

`COVERAGE_FLOOR` stays at **67** rather than moving to 68: measured is 68.7%,
and the Makefile's own comment argues for ~1 point of slack because the
whole-tree number drifts by a handful of statements between runs. 67 preserves
exactly the margin main already runs with (66 against a measured 67.8%).

```
$ make coverage
  coverage profile             ✓
  coverage gate (100%)         ✓
  coverage floor (67%)         ✓
$ awk '…' bin/coverage.gated.out     # 1483/1483 statements, all covered
```

## The rebase: what was re-adjudicated

This branch was ~3 weeks and 98 commits stale. Three commits change production
behaviour rather than adding tests, and each was re-checked against today's
`main` rather than assumed to still apply.

**`fix(sdk): stop leaking a full SDK copy when the extract race is lost` —
KEPT, bug still live.** Main landed `0a0c4c35` (one cache location and one
collection policy) and `d97e1cd6` (platform cache dir, not a hardcoded
`~/.cache`) in this file — the branch's only merge conflict. Main's structure
won: `defaultCacheDir` is `cache.Dir("sdks")`, and `sweepStale` runs on both
return paths. But main's `EnsureExtracted` still sets `success = true` in the
lost-race arm, and `cache.SweepAged` is called with `cache.NotDotted`, whose
own doc comment says it exists *because* "in-progress extractions are named
`.extract-*`". So main's new collector is specifically prevented from
collecting the directory this bug leaks. The one-line fix is still required and
is retained on top of main's structure.

**`fix(pkg): make install dedup agree with uninstall on unreadable entries` —
KEPT, nothing superseded it.** The brief expected main to have rewritten this
code via `bdc4dfeb`, `0dcb202b` and `3f7ae34d`. It did not: all three are
**ancestors of the branch point** (`699d5059`), so they were already in the
branch's base. `git log f34725fd -- pkg/pkg/` shows no commit newer than the
merge base at all — `pkg/pkg` is untouched on main since this branch forked.
Main's `containsPackage` still carries the unreachable verbatim fallback, and
main's `filterPackage` still matches verbatim first, so the asymmetry the fix
describes is exactly as live as it was three weeks ago. The fix now also
inherits main's subdir-aware `sourceIdentity` for free.

**`refactor(pkg): delete dead code and dedupe the local-path seam` — KEPT, the
deleted code is still dead.** `CurrentRef` has no referent anywhere in the tree
(`go vet ./...` and the full suite pass with it gone). `globPatterns`' discarded
`WalkDir` error is still unreachable — the closure still returns `nil` on every
error, and `WalkDir` only returns what the closure returns. `gitDirBase` and the
unconditionally-`nil` errors of `addPackage`/`removePackage` are likewise
unreferenced on today's main.

**One test rewritten.** `TestDefaultCacheDirNoHome` asserted the error string
`"sdk: resolve home dir"` and a `$HOME/.cache/fir/sdks` layout. `d97e1cd6`
deliberately replaced that with `pkg/cache`, which consults `$XDG_CACHE_HOME`
and then the platform cache dir. This is a behaviour main changed on purpose,
not a regression, so the test was rewritten rather than fixed: it is now
`TestDefaultCacheDirNoCacheRoot`, asserting the same *contract* (an unresolvable
cache root is a loud error naming the cause, not a silent extraction into the
working directory) against main's mechanism. `TestDefaultCacheDir` was
retargeted at the `$XDG_CACHE_HOME` arm, which also makes it deterministic on a
host that has that variable set — the old version would have failed there.

**No test was dropped.** Apart from the one rewrite above, the entire branch
suite compiles and passes against v1.11.0 unmodified.

**One changelog block was misfiled by the rebase and moved back.** Git matched
surrounding context and dropped this branch's four entries into the released
`[1.2.0]` section of `cmd/fir/CHANGELOG.md`, leaving `[Unreleased]` empty. No
conflict was raised. They are back under `[Unreleased]`, `[1.2.0]` is
byte-identical to main's again, and the diff against main is now purely
additive. This is the second time a rebase has done this to this branch — a
clean `git rebase` is not evidence that a changelog survived one.

## Bugs found

Two bugs fixed, one documented, one whole untested failure surface closed.
Tests at 100% surface these because the assertions that reach the last few
branches are the ones nobody writes.

1. **`pkg/extension/sdk`: a leaked SDK copy on every lost extraction race.**
   `EnsureExtracted` publishes the embedded SDK by renaming a temp directory
   into its content-addressed home. When another fir process wins, the rename
   fails with ENOTEMPTY and the loser adopts the winner's copy — but it also set
   `success = true`, suppressing the deferred cleanup of its *own* temp
   directory, which was then abandoned as `.extract-XXXXXX` forever. The comment
   ("prevent cleanup of already-renamed dir") described a danger that could not
   occur: the deferred `RemoveAll` only ever touches `tmp`. On a host running
   several fir sessions the cache grew without bound, and main's later sweeper
   skips dotted names by design. Fixed; caught by asserting that no `.extract-*`
   survives a simulated lost race, not by the error path.

2. **`pkg/pkg`: install-dedup and uninstall disagreed on identity.**
   `containsPackage`'s verbatim fallback for an unparseable settings entry sat
   after an early return that had already rejected the same string, so it was
   unreachable — while `filterPackage` *does* honour verbatim matches. An entry
   fir can no longer parse could be removed by its exact spelling but never
   recognised as installed, so re-installing appended a duplicate. Fixed.

3. **`pkg/pkg`: `git sparse-checkout set` exits 0 when it cannot prune.** With
   an unwritable working tree git only warns, so `Uninstall`'s shrink path can
   report success while the removed package's files stay on disk. Documented in
   a test comment; not fixed here (it needs a post-condition check, i.e. a
   behaviour change beyond this task).

4. **The gap — `pkg/log`'s failure paths were entirely untested.** No bug found,
   but the untested set included the one that would wedge rotation permanently
   (leaving `rotating` set after an aborted attempt), which no happy-path test
   could ever catch. Every failure path now asserts the flag is cleared.

## Testability seams and deletions

Minimal, and each defensible on its own terms.

**Seams (2):**

- `sdkFS` — an `fs.FS` package var defaulting to the embedded tree, beside the
  `cacheDir` var the file already used for exactly this purpose. `embed.FS`
  cannot fail a read or a listing, so every I/O error branch in the hash and
  extract walks was unreachable without it. Survived the rebase unchanged;
  main's refactor did not touch the walks.
- `debugEnvSet()` — `FIR_DEBUG` parsing split out of `pkg/log`'s `init()`, which
  runs once per process before any test can set the variable.

**Dead code deleted rather than covered:**

- `fnRandID` checked an error from `crypto/rand.Read`, which since Go 1.24 never
  returns one — it fills the buffer entirely or crashes the program. The module
  is on `go 1.25.0`.
- `gitDirBase`, `CurrentRef`, the unconditionally-`nil` errors of
  `addPackage`/`removePackage`, a duplicated local-path resolution, and a
  `WalkDir` error check whose callback swallows everything. All re-verified as
  still dead on v1.11.0.

No other production behaviour changed.

## `.covignore` entries added

**None** — in either section, for any of the seven packages. Section 2 lost
seven lines; section 1 is untouched.

## Standing constraint: run the suite unprivileged

Tests in `pkg/pkg`, `pkg/extension/sdk` and `pkg/log` force git and filesystem
failures with `chmod 0500` / `0444` / `0000`. As root those checks are no-ops
and the tests fail — and since these packages are now gated at 100%, they fail
the *build*, not just the coverage number. GitHub-hosted runners are non-root,
so this holds today; a root-based CI image would break it. Recorded in
`BACKLOG.md` and in the header comment of `pkg/pkg/git_errors_test.go`.

## Documentation updated

- `BACKLOG.md` — seven rows struck from the promotion queue, a "sealed since
  adoption" table added, the bucket table showing at-adoption vs today, the
  gated statement count updated (372 → 1,483), and the root constraint noted.
  `pkg/mcp/autoreply` is called out as the next cheapest (still 72 uncovered of
  253 on today's main — re-verified, not assumed).
- `Makefile` — `COVERAGE_FLOOR` 66 → 67 (measured 68.7%).
- `CONTRIBUTING.md` — "The coverage ratchet" section explains the two tiers and
  what sealing a package costs.
- `CHANGELOG.md` — entries under `## [Unreleased]`, with the statement counts
  corrected to the re-measured figures.

## Commits

Hashes are deliberately not listed — they change on every rebase, and this
file has already carried a stale set twice. Run `git log --oneline main..HEAD`.
The subjects, in order:

```
refactor(pkg): delete dead code and dedupe the local-path seam
fix(pkg): make install dedup agree with uninstall on unreadable entries
test(pkg): take pkg/pkg to 100% statement coverage
chore(coverage): seal pkg/pkg in the ledger, document the ratchet
docs(pkg): record the unprivileged-suite constraint and report the result
test(apikind): cover the ApiSpec handler registry, promote it out of the ledger
test(envkeys): cover credential detection, promote it out of the ledger
fix(sdk): stop leaking a full SDK copy when the extract race is lost
test(declcfg): cover the substitution grammar, promote it out of the ledger
test(agent/tools): cover the schema codec, promote it out of the ledger
test(log): cover rotation failure paths, promote it out of the ledger
chore(coverage): record the cleared on-ramp and ratchet the floor
docs(coverage): stop the ledger header keeping its own list of sealed packages
test: review pass — fix a misreporting panic helper, drop three redundancies
docs(coverage): correct the report against the post-rebase tree
test(log): fold the gzip round-trip into its error test, mirroring copyFile
```

Plus the rebase commit that follows this file, carrying the `extract.go`
conflict resolution, the rewritten cache-dir tests, the changelog relocation
and the re-measured figures throughout.

## Next cheapest

`pkg/mcp/autoreply` (72 uncovered of 253), then `pkg/session/compaction`
(77/600) and `pkg/auth` (130/625). All three re-measured on v1.11.0.
