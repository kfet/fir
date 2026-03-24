#!/usr/bin/env bash
#
# Generic runtime wrapper for JS/TS/Python extensions without shebangs.
# Shipped with fir SDK, symlinked into extension directories by the install extension.
#
# This script is symlinked into extension directories by the install extension.
# It auto-detects the entry point and runtime, then execs the extension.
#
# Symlink convention:
#   <ext-dir>/main  →  <path-to>/run.sh
#   <ext-dir>/main.ts   (the actual extension)
#
# Discovery order for entry point (first match wins):
#   1. index.ts, index.js  (standard JS convention)
#   2. main.ts, main.js    (fir convention)
#   3. <dirname>.ts, <dirname>.js  (name-based)
#   4. First *.ts, then first *.js alphabetically
#
# Runtime detection (per file extension):
#   .ts → bun run | npx tsx | node --experimental-strip-types
#   .js → bun run | node
#   .py → python3 | python
#
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
DIRNAME="$(basename "$DIR")"

# Locate the SDK dir. run.sh is extracted alongside fir_ext.js and pi_compat.js,
# so follow the symlink back to the real script location.
SCRIPT_REAL="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || echo "$0")"
SDK_DIR="$(cd "$(dirname "$SCRIPT_REAL")" && pwd)"

# ── Find entry point ──────────────────────────────────────────────────────

ENTRY=""

find_entry() {
  local dir="$1"
  local name="$2"

  for candidate in \
    "$dir/index.ts" "$dir/index.js" \
    "$dir/main.ts" "$dir/main.js" \
    "$dir/${name}.ts" "$dir/${name}.js"; do
    if [ -f "$candidate" ]; then
      ENTRY="$candidate"
      return 0
    fi
  done

  # Fallback: first .ts then first .js alphabetically
  ENTRY="$(find "$dir" -maxdepth 1 -name '*.ts' -not -name '*.d.ts' | sort | head -1)"
  [ -n "$ENTRY" ] && return 0
  ENTRY="$(find "$dir" -maxdepth 1 -name '*.js' | sort | head -1)"
  [ -n "$ENTRY" ] && return 0

  # Also check for .py
  ENTRY="$(find "$dir" -maxdepth 1 -name '*.py' | sort | head -1)"
  [ -n "$ENTRY" ] && return 0

  return 1
}

if ! find_entry "$DIR" "$DIRNAME"; then
  echo "run.sh: no entry point found in $DIR" >&2
  exit 1
fi

# ── Detect if this is a pi-mono extension ─────────────────────────────────

is_pi_mono_ext() {
  # Check if the entry point imports from pi-mono SDK
  grep -qE '(from\s+["'"'"']@mariozechner/pi-coding-agent|require\(["'"'"']@mariozechner/pi-coding-agent)' "$1" 2>/dev/null
}

# ── Exec with appropriate runtime ─────────────────────────────────────────

EXT="${ENTRY##*.}"

case "$EXT" in
  ts|js)
    if is_pi_mono_ext "$ENTRY" && [ -n "$SDK_DIR" ] && [ -f "$SDK_DIR/pi_compat.js" ]; then
      # Pi-mono extension → run through compat shim
      COMPAT="$SDK_DIR/pi_compat.js"
      if command -v bun >/dev/null 2>&1; then
        exec bun run "$COMPAT" "$ENTRY"
      elif command -v node >/dev/null 2>&1; then
        if [ "$EXT" = "ts" ]; then
          # Need tsx for TypeScript
          if command -v npx >/dev/null 2>&1; then
            exec npx tsx "$COMPAT" "$ENTRY"
          else
            exec node --experimental-strip-types "$COMPAT" "$ENTRY"
          fi
        else
          exec node "$COMPAT" "$ENTRY"
        fi
      fi
    else
      # Native fir extension or unknown → run directly
      if command -v bun >/dev/null 2>&1; then
        exec bun run "$ENTRY"
      elif command -v node >/dev/null 2>&1; then
        if [ "$EXT" = "ts" ]; then
          if command -v npx >/dev/null 2>&1; then
            exec npx tsx "$ENTRY"
          else
            exec node --experimental-strip-types "$ENTRY"
          fi
        else
          exec node "$ENTRY"
        fi
      fi
    fi
    echo "run.sh: no JS/TS runtime found (install bun or node)" >&2
    exit 1
    ;;
  py)
    if command -v python3 >/dev/null 2>&1; then
      exec python3 "$ENTRY"
    elif command -v python >/dev/null 2>&1; then
      exec python "$ENTRY"
    fi
    echo "run.sh: no Python runtime found" >&2
    exit 1
    ;;
  *)
    # Try direct execution
    if [ -x "$ENTRY" ]; then
      exec "$ENTRY"
    fi
    echo "run.sh: don't know how to run $ENTRY" >&2
    exit 1
    ;;
esac
