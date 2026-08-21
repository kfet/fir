# `pkg/pkg` → 100%, and the coverage ratchet

Branch: `test/pkg-pkg-coverage` (worktree `~/fir-covpkg`), 4 commits on top of
`624b4442`. Not merged, not pushed. `make all` is green.

## Before / after

| Scope | Before | After |
| --- | ---: | ---: |
| `pkg/pkg` (package's own tests, `go test ./pkg/pkg/...`) | 69.6% | **100.0%** |
| `pkg/pkg` (whole-repo profile, 518 statements) | ~69–72%¹ | **100.0%** |
| Whole tree (`COVERAGE_FLOOR` tier) | 67.0% | **67.5%** |
| Gated at a hard `-min=100` (tier 1) | 372 statements | **890 statements** |

¹ The brief's 71.9% came from a whole-repo profile, where other packages' tests
incidentally execute some of `pkg/pkg`; the package's own suite measured 69.6%.
Both are now 100.0%.

Test code added: ~2,000 lines across `resources_test.go`, `git_errors_test.go`,
`manager_extra_test.go`, `parse_extra_test.go`. No test touches the network and
none skips: every git path runs against a bare repo built in `t.TempDir()`,
with a git `insteadOf` rewrite standing in for `https://github.com/`.

## The gate

**It already existed.** The brief's premise was one commit stale: `d85651e0`
("feat: adopt the covgate coverage gate with an honest exclusion ledger")
landed the acp-kit idiom on `main` before this task started — `covgate` pinned
as a go.mod `tool` directive (`go tool covgate`, v0.1.2), a two-tier `coverage`
target in the Makefile (`-ignore=.covignore -min=100`, plus a whole-tree
`COVERAGE_FLOOR` floor), already wired into `_all_parallel` and therefore into
`make all`. Re-introducing it would have been a duplicate.

So the work was the other half: **being the first package to leave the ledger.**
`^github\.com/kfet/fir/pkg/pkg/[^/]+\.go:` is deleted from section 2 of
`.covignore`; the gate now enforces `pkg/pkg` at 100% forever.

## Permanent (`.covignore`) entries added

**None.** Not one line, in either section.

That is a deliberate result, not an oversight. fir's `.covignore` states its own
convention in its header — *file- or directory-level patterns only, no line
numbers, no per-function regexes* — because line numbers rot on the first edit
above them and per-function regexes silently mask new untested code added inside
the same function. The harb-style line-range regexes the brief offered as
acceptable would have contradicted the ledger fir actually uses, so every
uncovered branch was either reached by a test or deleted as dead code (below).

For the record, the branches that *tempted* an exclusion, and how they were
resolved instead:

| Branch | Resolution |
| --- | --- |
| `filepath.WalkDir` error in `globPatterns` | Deleted — the callback swallows every error and returns nil unconditionally; `WalkDir` returns only what the callback returns, so it could never be non-nil. |
| `filepath.Rel` error in `autoDiscover` | Deleted — `Rel` cannot fail for paths `WalkDir` yields under the same root. Replaced by a direct cleaned-parent comparison. |
| `if err != nil` after `addPackage`/`removePackage` | Deleted — both returned `nil` unconditionally. |
| `if existing == source` in `containsPackage` | **Was a real bug**, not dead weight. See below. |
| `sparse-checkout disable` / `set` failures | Covered for real: a read-only working tree and an unwritable `.git/info` respectively. |
| `Pull` fetch-vs-merge error precedence | Covered by diverging a clone from a moved origin with a broken remote URL. |

## Standing constraint: run the suite unprivileged

Several new tests force a git or filesystem failure by making a directory
unwritable (`chmod 0500`). As root those permission checks are no-ops and the
tests fail — and because `pkg/pkg` is now sealed at 100%, they fail the build
rather than merely dropping coverage. GitHub-hosted runners are non-root, so
this holds today; it is written down here and in the header comment of
`pkg/pkg/git_errors_test.go` because the gate now enforces it. A root-based CI
image would be the thing that breaks it.

## Bugs and smells found

1. **Install-dedup and uninstall disagreed about identity** (fixed, `5cf83da5`).
   `containsPackage`'s verbatim fallback for a settings entry fir can no longer
   parse sat *after* an early return that had already rejected the same string
   on a parse failure — reaching it required one string to both parse and not
   parse within a single call. `filterPackage` *does* honour a verbatim match
   when removing. Net effect: an entry fir cannot parse (hand-edited settings, a
   source syntax fir has since dropped) could be removed by its exact spelling
   but was never recognised as already installed, so re-installing it appended a
   duplicate. Fixed by resolving the candidate's identity the way `filterPackage`
   does and running the verbatim check before any parse.

2. **`git sparse-checkout set` exits 0 when it cannot prune a directory.** Found
   while trying to make `SparseSet` fail: with a read-only working tree git
   prints `warning: failed to remove directory 'sub/'` and still succeeds. So
   `Uninstall`'s shrink path can report success while the removed package's
   files remain on disk. Not fixed here (it would need a post-condition check
   after the shrink, i.e. a behaviour change beyond this task's remit), but the
   test comment records it and it is worth a BACKLOG entry.

3. **`SettingsBackend` cannot report a persistence failure.** `SetGlobalPackages`
   / `SetProjectPackages` return nothing, so a settings write that fails is
   invisible to `Install`/`Uninstall`. The `error` returns on
   `addPackage`/`removePackage` looked like handling but were unconditionally
   `nil`. Removed rather than left as decoration — if writes can fail for real,
   the fix belongs in the interface.

4. **Dead code:** `gitDirBase` (unexported, unreferenced, and its doc comment
   claimed it was "exported for test convenience" — it was neither) and
   `CurrentRef` (exported, referenced nowhere in the tree). Both deleted; fir is
   an application module and `pkg/` carries no API guarantee, so "exported" was
   not a reason to keep an unused function.

## Refactors for testability

Minimal, and each one is an improvement on its own terms — no seams or
interfaces were injected, and no production behaviour changed except where
noted.

- `ParseSource` resolved a local path with duplicated code in two places; both
  call sites now go through one `parseLocal`.
- `autoDiscover`'s "is this file at the package root" test moved from
  `filepath.Rel` + `SplitN` + a dead error branch to a cleaned-parent
  comparison. The one behavioural corner this touched — a package that *is* a
  single file, where the file itself is at root — was preserved deliberately and
  is now pinned by `TestAutoDiscoverSingleFilePackage`, which did not exist
  before.
- `addPackage`/`removePackage` lost their unconditionally-`nil` `error` returns.
- `globPatterns` discards the `WalkDir` result with a comment explaining why it
  is always `nil`.

## Where the ratchet is documented

- `CONTRIBUTING.md` — new "The coverage ratchet" section: the two tiers, that
  `.covignore` is a debt ledger, that sealing a package is one commit (cover,
  delete the line, watch the gate pass), that adding a line needs a
  justification, and that the non-recursive patterns mean a **new package is
  gated at 100% from its first commit**.
- `BACKLOG.md` — the promotion is recorded, `pkg/pkg` struck from the queue, and
  the gated statement count updated (372 → 890) since that count is one of the
  three numbers the backlog says must move monotonically.
- `CHANGELOG.md` — entries under `## [Unreleased]`.

## Commits

```
506199ee refactor(pkg): delete dead code and dedupe the local-path seam
5cf83da5 fix(pkg): make install dedup agree with uninstall on unreadable entries
046ece59 test(pkg): take pkg/pkg to 100% statement coverage
99b8eda2 chore(coverage): seal pkg/pkg in the ledger, document the ratchet
```

## Verification

```
$ go test -covermode=set -coverprofile=bin/coverage.tmp.out ./...
$ go tool covgate -profile=bin/coverage.tmp.out -out=bin/coverage.gated.out \
      -ignore=.covignore -min=100
coverage: 100.0% of statements (890/890)
$ go test -race -shuffle=on ./pkg/pkg/          # clean
$ make all                                      # green
```
