---
name: release
description: Release a new version of tau. Confirms reviews and tests pass, updates VERSION and CHANGELOG.md, commits, tags, and runs make install.
---

# Release Skill

Release a new version of tau. The user provides the new version number (e.g. `0.2.0`).

## Steps

1. **Full build & test** — execute `make all` (runs test-race, PGO, cross-compile) and confirm everything passes.
2. **E2E tests** — run the e2e skill and confirm all scenarios pass. Use the e2e skill at `.tau/skills/e2e/SKILL.md`.
3. **Check CHANGELOG** — read `CHANGELOG.md` and confirm there are entries under `## [Unreleased]`. If empty, ask the user whether to proceed or abort.
4. **Update CHANGELOG** — rename `## [Unreleased]` to `## [VERSION] - YYYY-MM-DD` (today's date) and add a fresh empty `## [Unreleased]` section above it.
5. **Update VERSION** — write the new version to the `VERSION` file (no trailing newline beyond one).
6. **Commit** — stage **all** uncommitted changes (not just VERSION/CHANGELOG) with `git add -A`, then commit with `git commit -m "release: vVERSION"`. Check `git status` first to make sure nothing unexpected is staged.
7. **Tag** — use `git tag -a vVERSION -m "release: vVERSION"` (pass `-m` to avoid git opening an editor like vim, which breaks in non-interactive environments).
8. **Install** — `make install` to install the new version.
9. **Verify** — run `tau --version` and confirm it prints the new version.

## Important notes

- **Uncommitted changes**: Always check `git status` before committing. All release-related and pending changes should be included in the release commit.
- **Avoid interactive git**: Always pass `-m` to `git tag -a` and `git commit`. Git may try to open vim/nano for messages, which fails in non-interactive environments.
- **Moving tags**: If you need to move a tag after an additional commit, use `git tag -d vVERSION` then re-create it.

If any step fails, stop and report the error. Do not push — the user decides when to push.
