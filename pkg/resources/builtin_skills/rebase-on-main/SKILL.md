---
builtin: true
name: rebase-on-main
description: Rebase the current feature branch onto local main to pick up recent main changes without merging. Verifies no main content is lost during conflict resolution.
override: true
---

1. `git rebase main`. When conflicts arise, preserve main's content and re-apply only the feature's intended changes on top.
2. **Verify the diff** — `git diff main..HEAD`. Confirm it contains only the feature's intended changes and nothing that removes or reverts existing main content. If main changes were dropped, abort and fix.
3. **Update CHANGELOG.md** — ensure all commits being rebased have corresponding entries in the `[Unreleased]` section. Add any missing ones under the appropriate subsection (Added/Changed/Fixed/Removed). **Verify they are under `## [Unreleased]`, not merely present**: rebasing across a release boundary silently relocates them into the just-released section, conflict-free, and step 2's diff still looks clean (pure additions, zero deletions). Read `sed -n '1,40p' CHANGELOG.md` and confirm each entry sits above the first `## [x.y.z]` heading; move any that don't.
4. **Build** — run `make all` to confirm nothing broke.
