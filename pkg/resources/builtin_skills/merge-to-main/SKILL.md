---
builtin: true
name: merge-to-main
description: Merge a feature branch to main — squash, rebase onto local main, verify no main changes lost, ff-merge into the main worktree.
override: true
---

Merge the current feature branch into local main with a clean, linear history.

0. **Check main is current** — `git fetch origin` and confirm local `main` is
   not behind `origin/main` (`git rev-list --count main..origin/main` must be
   `0`). If it is behind, fast-forward local main first and rebase this branch
   onto it. Merging — and especially cutting a release — on a stale base means
   redoing it: the version may already be taken upstream.
1. **Squash** if too many commits.
2. **Rebase onto local main.**
3. **Verify the diff** — run `git diff main..HEAD`. Confirm it contains only the feature's intended changes and nothing that removes or reverts existing main content. If main changes were dropped during conflict resolution, fix before proceeding.
4. **Build** — run `make all` to confirm nothing broke.
5. **Update CHANGELOG.md** — ensure all commits in the branch have corresponding entries in the `[Unreleased]` section. Compare `git log main..HEAD --oneline --no-merges` against existing entries; add any missing ones under the appropriate subsection (Added/Changed/Fixed/Removed). **Check the entries are under `## [Unreleased]`, not merely present**: a rebase across a release boundary relocates them into the just-released section with no conflict, and `git diff main..HEAD` still shows a clean pure-addition diff. Read the top of the file (`sed -n '1,40p' CHANGELOG.md`) and confirm with your own eyes that each new line sits above the first `## [x.y.z]` heading; move any that don't.
6. **FF-merge** into the main worktree.
7. **Build again** — run `make all` from the main worktree to confirm the merge is clean.

At the end ask the user if they want to clean-up by removing the local worktree and branch. Careful, this could be destructive!
