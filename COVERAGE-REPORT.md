# Coverage-gate on-ramp: queue items 1-6 sealed

Branch: `test/pkg-pkg-coverage` (worktree `~/fir-covpkg`), rebased onto local
`main` at v1.1.1. Not merged, not pushed. `make all` is green on that base.

## Result

The whole on-ramp from `BACKLOG.md` is cleared, in queue order, one commit per
package. Nothing was added to `.covignore` to achieve it.

| # | Package | Before | After | Gated statements |
| ---: | --- | ---: | ---: | ---: |
| 1 | `pkg/extension/apikind` | 0.0% | **100.0%** | 6 |
| 2 | `pkg/ai/envkeys` | 61.7% | **100.0%** | 47 |
| 3 | `pkg/extension/sdk` | 67.2% | **100.0%** | 64 |
| 4 | `pkg/ai/providers/declcfg` | 82.8% | **100.0%** | 122 |
| 5 | `pkg/agent/tools` | 77.8% | **100.0%** | 117 |
| 6 | `pkg/log` | 77.9% | **100.0%** | 199 |
| 12 | `pkg/pkg` (done before the retarget) | 69.6% | **100.0%** | 518 |

| Metric | At adoption | Now |
| --- | ---: | ---: |
| Gated at a hard `-min=100` | 372 statements (1.2%) | **1,443 (4.6%)** |
| Excluded — structural | 12,446 (39.5%) | 12,446 (39.5%) |
| Excluded — pure debt | 18,677 (59.3%) | **17,602 (55.9%)** |
| Whole tree (`COVERAGE_FLOOR` tier) | 67.0% | **68.1%** |
| `COVERAGE_FLOOR` | 66 | **67** |

Every number above is from `make coverage`, not from eyeballing: each package
was promoted only after the gate itself passed with its `.covignore` line
deleted.

```
$ make coverage
  coverage profile             ✓
  coverage gate (100%)         ✓
  coverage floor (67%)         ✓
$ awk '…' bin/coverage.gated.out     # 1443/1443 statements, all covered
```

## A note on `pkg/pkg`

The first five commits on this branch took `pkg/pkg` (queue item 12) to 100%
and sealed it, before the retarget. They are **kept**, not reverted: the
package is at 100%, the gate enforces it, and the work found a real bug. It is
out of queue order, which is recorded as such in `BACKLOG.md`. Say the word and
it comes off the branch, but deleting a sealed package to restore queue purity
seemed the worse trade.

## Bugs found

Three, plus one gap. None cosmetic. Tests at 100% surface these because the
assertions that reach the last few branches are the ones nobody writes.

1. **`pkg/extension/sdk`: a leaked SDK copy on every lost extraction race.**
   `EnsureExtracted` publishes the embedded SDK by renaming a temp directory
   into its content-addressed home. When another fir process wins, the rename
   fails with ENOTEMPTY and the loser adopts the winner's copy — but it also set
   `success = true`, suppressing the deferred cleanup of its *own* temp
   directory, which was then abandoned as `.extract-XXXXXX` forever. The comment
   ("prevent cleanup of already-renamed dir") described a danger that could not
   occur: the deferred `RemoveAll` only ever touches `tmp`. On a host running
   several fir sessions the cache grew without bound. Fixed; caught by asserting
   that no `.extract-*` survives a simulated lost race, not by the error path.

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
  extract walks was unreachable without it.
- `debugEnvSet()` — `FIR_DEBUG` parsing split out of `pkg/log`'s `init()`, which
  runs once per process before any test can set the variable.

**Dead code deleted rather than covered (in the retargeted work):**

- `fnRandID` checked an error from `crypto/rand.Read`, which since Go 1.24 never
  returns one — it fills the buffer entirely or crashes the program. The module
  is on `go 1.25.0`.
- (In the earlier `pkg/pkg` commits: `gitDirBase`, `CurrentRef`, the
  unconditionally-`nil` errors of `addPackage`/`removePackage`, a duplicated
  local-path resolution, and a `WalkDir` error check whose callback swallows
  everything.)

No other production behaviour changed.

## `.covignore` entries added

**None** — in either section, for any of the seven packages. Section 2 lost six
lines (plus `pkg/pkg`'s earlier); section 1 is untouched.

## Standing constraint: run the suite unprivileged

Tests in `pkg/pkg`, `pkg/extension/sdk` and `pkg/log` force git and filesystem
failures with `chmod 0500` / `0444` / `0000`. As root those checks are no-ops
and the tests fail — and since these packages are now gated at 100%, they fail
the *build*, not just the coverage number. GitHub-hosted runners are non-root,
so this holds today; a root-based CI image would break it. Recorded in
`BACKLOG.md` and in the header comment of `pkg/pkg/git_errors_test.go`.

## Documentation updated

- `BACKLOG.md` — six rows struck from the promotion queue, a "sealed since
  adoption" table added, the bucket table now shows at-adoption vs today, the
  gated statement count updated (372 → 1,443), and the root constraint noted.
  `pkg/mcp/autoreply` is called out as the next cheapest.
- `Makefile` — `COVERAGE_FLOOR` 66 → 67 (measured 68.1%, keeping the ~1 point of
  slack the surrounding comment argues for, so the tier does not flap).
- `CONTRIBUTING.md` — "The coverage ratchet" section (added with the `pkg/pkg`
  work) explains the two tiers and what sealing a package costs.
- `CHANGELOG.md` — entries under `## [Unreleased]`.

## Commits

Hashes are deliberately not listed — they change on every rebase, and this
file has already carried a stale set once. Run `git log --oneline main..HEAD`.
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

## Review pass

The branch was reviewed against its own diff and rebased onto current `main`
(19 commits ahead, up to v1.1.1) so the gate proves the sealed packages against
the tree they will land on rather than a stale one — a gate result is a
property of a tree, not of a diff. Main had touched none of the sealed
packages. Two things came out of it:

- **A test helper misreported panics.** `mustPanic` rendered the recovered
  value through a type switch that returned `""` for anything that was not a
  `string` or an `error`, and the caller read `""` as "no panic". A panic with
  an unexpected type would have been reported as *"expected a panic, got
  none"* — an inverted diagnosis of the exact thing the helper exists to catch.
- **The rebase silently misfiled this branch's CHANGELOG entries** into the
  released `[0.99.0]` section, because git matched surrounding context after
  main's releases moved the old `[Unreleased]` block down. No conflict was
  raised. Moved back under `[Unreleased]`; `[0.99.0]` is byte-identical to
  main's again. Worth knowing: a clean `git rebase` is not evidence that a
  changelog survived one.

Three redundancies were also removed (a local `equalStrings` that stdlib
`slices.Equal` already provides, a dead local plus a comment narrating an
approach a test does not take, and a hand-rolled logger save/restore where the
package already had `resetLogger()`).

## Next cheapest

`pkg/mcp/autoreply` (72 uncovered of 253), then `pkg/session/compaction`
(77/600) and `pkg/auth` (134/525).
