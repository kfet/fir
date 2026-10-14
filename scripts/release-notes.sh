#!/usr/bin/env bash
# Print the GitHub release body for a tag: that version's CHANGELOG section
# as highlights, followed by the compare link to the previous tag.
#
#   scripts/release-notes.sh v1.35.2 [CHANGELOG.md] [previous-tag]
#
# Used by .github/workflows/release.yml (goreleaser --release-notes) and for
# backfilling old releases with `gh release edit --notes-file`.
set -euo pipefail

TAG="${1:?usage: release-notes.sh <tag> [changelog] [prev-tag]}"
CHANGELOG="${2:-cmd/fir/CHANGELOG.md}"
PREV="${3:-}"
VER="${TAG#v}"
REPO="${GITHUB_REPOSITORY:-kfet/fir}"

if [ -z "$PREV" ]; then
  PREV=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "${TAG}^" 2>/dev/null || true)
fi

# Section body: lines after "## [VER]" up to the next "## [" heading,
# with leading/trailing blank lines trimmed.
SECTION=$(awk -v v="$VER" '
  index($0, "## [" v "]") == 1 { on = 1; next }
  on && /^## \[/ { exit }
  on { print }
' "$CHANGELOG" | sed -e '/./,$!d' | sed -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')

if [ -n "$SECTION" ]; then
  printf '## Highlights\n\n%s\n\n' "$SECTION"
fi
if [ -n "$PREV" ]; then
  printf '**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$REPO" "$PREV" "$TAG"
fi
