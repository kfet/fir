#!/usr/bin/env bash
# snapshot.sh — collect a quick project status snapshot
#
# Usage: ./snapshot.sh [PROJECT_ROOT]

set -euo pipefail

ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$ROOT"

echo "=== SNAPSHOT @ $(date '+%H:%M:%S') ==="

echo "--- Modified (last 3 min) ---"
git diff --name-only 2>/dev/null || true
git ls-files --others --exclude-standard 2>/dev/null | head -20

echo "--- Recent on disk ---"
find . -not -path './.git/*' -not -path './node_modules/*' -not -path './vendor/*' \
  -type f -mmin -3 2>/dev/null | sort

echo "--- Tests ---"
if [ -f Makefile ] && grep -q '^test:' Makefile; then
  make test 2>&1 | tail -15
else
  echo "(no test target detected — add a 'test' Makefile target or override this script)"
fi
