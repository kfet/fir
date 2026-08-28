---
name: release
description: Release a new version. Confirms reviews and tests pass, updates VERSION and CHANGELOG.md, commits, tags, and installs.
override: true
---

# Release Skill

Release a new version of the project.

## Version determination

If the user provides a version, use it. Otherwise, auto-determine:

1. Read the current version from `VERSION`.
2. Look at entries under `## [Unreleased]` in `CHANGELOG.md`.
3. If there are `### Added` or `### Removed` entries → **minor** bump (e.g. 0.1.0 → 0.2.0).
4. If there are only `### Fixed` or `### Changed` entries → **patch** bump (e.g. 0.1.0 → 0.1.1).
5. If the section is empty → ask the user whether to proceed or abort.

A regenerated model catalog (step 1) does **not** count towards the bump — it is
routine data, not a user-visible feature, so it never turns a patch into a minor.
By convention it still gets the one-line `### Changed` catalog entry the previous
releases carry; that line alone is not grounds for a minor bump either.

## Steps

1. **Regenerate models** — run `make generate-models` to pull the latest model definitions. If this produces changes, they will be included in the release commit automatically.
2. **Full build & test** — execute `make all` and confirm everything passes.
3. **Check CHANGELOG** — read `CHANGELOG.md` and confirm there are entries under `## [Unreleased]`. If empty, ask the user.
4. **Determine version** — follow the rules above if the user didn't specify one. State the version and proceed.
5. **Update CHANGELOG** — rename `## [Unreleased]` to `## [VERSION] - YYYY-MM-DD` (today's date) and add a fresh empty `## [Unreleased]` section above it. Keep reverse-chronological order.
6. **Update VERSION** — write the new version to the `VERSION` file (no trailing newline beyond one).
7. **Commit** — stage **all** uncommitted changes with `git add -A`, then commit with `git commit -m "release: vVERSION"`. Check `git status` first.
8. **Tag** — use `git tag -a vVERSION -m "release: vVERSION"` (pass `-m` to avoid opening an editor).
9. **Install** — `make install` to install the new version.
10. **Verify** — run **the binary `make install` just wrote**, by absolute path, and confirm it prints the new version:

    ```bash
    # go install honours GOBIN when set, else GOPATH/bin.
    installdir="$(go env GOBIN)"; [ -n "$installdir" ] || installdir="$(go env GOPATH)/bin"
    "$installdir/fir" --version
    ```

    Then reconcile PATH: compare `command -v fir` against `$installdir/fir`. If
    they differ (and are not the same file — a symlink is fine), PATH is serving
    a **stale, shadowing** binary and a bare `fir --version` will silently report
    the old version. `make install` warns about this; do not ignore the warning.
    Report which path holds which version. Only if the shadowing path is
    user-writable and clearly meant to be the same install, offer to overwrite it
    (`cp "$installdir/fir" "$(command -v fir)"`, keeping a `.prev` backup as
    `make deploy` does) — never do so silently, and never assume it is writable.
    Never accept a bare `fir --version` as proof.

## Important notes

- **Uncommitted changes**: Always check `git status` before committing. All release-related and pending changes should be included in the release commit.
- **Avoid interactive git**: Always pass `-m` to `git tag -a` and `git commit`. Git may try to open vim/nano, which fails in non-interactive environments.
- **Moving tags**: If you need to move a tag after an additional commit, use `git tag -d vVERSION` then re-create it.

## Publishing

After the user confirms, run `make publish` to regenerate the PGO profile and amend the release commit if it changed, push the commit and tag to origin, and let GoReleaser CI build and create the release.

Alternatively, `make deploy` pushes binaries directly to remote hosts via scp (no GitHub release needed).

If any step fails, stop and report the error. Do not push or publish unless the user confirms.

**Before publishing, re-check that `origin/main` has not moved** since you
started — another agent may be releasing the same repo concurrently. `git
fetch origin` and confirm your branch is not behind.

**If the push is rejected:** discard the *release*, keep the *work*. Delete
the tag (`git tag -d vVERSION`), unwind the release commit, rebase onto the
new `origin/main`, then redo the release from the start — re-pick the
version (theirs may have taken it), re-derive the CHANGELOG section from
`## [Unreleased]`, and re-run `make all` on the merged tree. Never rebase a
finalised release commit and push it: the version, changelog and test run
are assertions about a tree that no longer exists.

## Post-publish: Track All GitHub Actions

After `make publish` succeeds, poll GitHub Actions until every triggered workflow finishes:

```bash
SHA=$(git rev-parse HEAD)   # the release commit; runs are matched on this

# env -u ...: a shell that exports CLICOLOR_FORCE / FORCE_COLOR / CLICOLOR makes
# `gh` emit ANSI escapes *into its --json output*, so jq fails with
# "Invalid numeric literal". NO_COLOR=1 does NOT override CLICOLOR_FORCE, and
# gh's own --jq is colourised too — unsetting the forcing vars is the fix.
# No 2>&1 either: merging stderr into output that must parse as JSON turns a
# transient gh error into unparseable garbage.
GH="env -u CLICOLOR_FORCE -u FORCE_COLOR -u CLICOLOR gh"

runs=$($GH run list --limit 15 --json status,conclusion,name,headSha,databaseId) || {
  echo "PROBE ERROR: gh run list failed"; exit 1; }
echo "$runs" | jq -e . >/dev/null || { echo "PROBE ERROR: output is not JSON"; exit 1; }
echo "$runs" | jq -r --arg sha "$SHA" '.[] | select(.headSha==$sha) | "\(.name) \(.status) \(.conclusion)"'
```

`$SHA` must be set, and the selection must be non-empty: zero matching runs is
"probe not working yet", **not** "all runs finished". Treat an empty match as
not-ready early on, and as an error if it persists.

This must **not** use `--branch` filtering — tag-triggered workflows (like `release`) don't appear under a branch filter. Instead, match runs by `headSha` against the release commit.

Loop every 30 seconds. Stop when all runs for the release commit SHA are `completed`. If any conclude with `failure` or `cancelled`, report the failure details:

```bash
$GH run view <run-id> --log-failed | tail -40
```

### Probe hygiene (applies to any poll loop)

**A probe that errored or produced unparseable output is a FAILURE, not a
"keep waiting".** Treat these as three distinct outcomes and never collapse them:

- **ready** → stop, success.
- **not ready** → the probe ran, parsed cleanly, and the state is genuinely
  still in progress. Only this continues the loop.
- **errored / unparseable** → a broken probe. Retry at most 2–3 *consecutive*
  times (network blips are real), then **stop and report**. Never let it fall
  through to "continue" indefinitely.

Concretely: check the command's exit code before piping to `jq`; validate with
`jq -e .` before extracting fields; never pipe a command's stderr (`2>&1`) into
something that must parse as structured data; and **never assume a `--json` flag
yields clean JSON** — a colour-forcing environment, a pager, or a progress
spinner can all corrupt it, and the tool still exits 0. If you cannot distinguish
"not ready" from "broken", the loop is wrong — a broken probe polls happily until
timeout while the thing it watches has long since finished. Also bound the loop
(a wall-clock cap and a max poll count) so even a mis-classified probe cannot
run for the better part of an hour.

When a probe *does* come back unparseable, look at the raw bytes
(`printf '%s' "$out" | od -c | head`) before assuming it was transient. An ANSI
escape or a BOM at byte 0 is a permanent, reproducible fault that no amount of
retrying will clear.

Do not ask the user whether to monitor — always do it automatically after a successful publish.
