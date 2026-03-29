---
name: rebase-on-main
description: Rebase the current feature branch onto local main to pick up recent main changes without merging. Verifies no main content is lost during conflict resolution.
---

1. `git rebase main`. When conflicts arise, preserve main's content and re-apply only the feature's intended changes on top.
2. **Verify the diff** — `git diff main..HEAD`. Confirm it contains only the feature's intended changes and nothing that removes or reverts existing main content. If main changes were dropped, abort and fix.
3. **Build** — run `make all` to confirm nothing broke.
