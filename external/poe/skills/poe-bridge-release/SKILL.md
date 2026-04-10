---
name: poe-bridge-release
description: Tag and publish a new poe-bridge release. Bumps the version in main.go, commits, tags as poe-bridge/vX.Y.Z, and pushes — CI handles the rest (cross-compile + GitHub Release with binaries). Use when the user asks to release/publish the poe bridge.
---

# poe-bridge-release

Publish a new poe-bridge release to GitHub.

## Steps

1. **Decide the version** — follow semver. Check the current version:
   ```
   grep 'version ' external/poe/cmd/poe-bridge/main.go | head -1
   ```

2. **Bump the version** in `external/poe/cmd/poe-bridge/main.go`:
   ```go
   version = "X.Y.Z"
   ```

3. **Run the full test suite**:
   ```
   make -C external/poe fmt-check vet test-race build-all
   ```

4. **Commit the version bump**:
   ```
   git add external/poe/cmd/poe-bridge/main.go
   git commit -m "external/poe: bump version to vX.Y.Z"
   ```

5. **Tag the release** — the tag MUST use the `poe-bridge/v` prefix:
   ```
   git tag -a poe-bridge/vX.Y.Z -m "poe-bridge vX.Y.Z"
   ```

6. **Push the commit and tag**:
   ```
   git push origin <branch> poe-bridge/vX.Y.Z
   ```

7. **CI takes over** — `.github/workflows/bridge-poe-release.yml` triggers
   on the `poe-bridge/v*` tag, cross-compiles for all 5 targets, and
   creates a GitHub Release with the binaries as assets.

8. **Verify** — check https://github.com/kfet/fir/releases for the new
   `poe-bridge vX.Y.Z` release with 5 binary assets.

## Notes

- The release workflow is completely independent from fir's own release.
- Running bridges can self-update: `poe-bridge --self-update` or the
  background startup check will log when a new version is available.
- The tag format `poe-bridge/vX.Y.Z` is required — the release workflow
  only triggers on this pattern and extracts the version from it.
