#!/bin/bash
# Usage: ./.fir/skills/sync/scripts/sync-record.sh ../pi-mono
# Records hash baseline for tracked TS source files only

UPSTREAM="$1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT="$SCRIPT_DIR/../data/.baseline-hashes"

if [ -z "$UPSTREAM" ]; then
    echo "Usage: $0 <path-to-pi-mono>"
    exit 1
fi

# Only track files that matter — AI layer, providers, oauth, agent loop, model/prompt core
TRACKED_DIRS=(
    "packages/ai/src"
    "packages/ai/scripts"
    "packages/agent/src"
    "packages/coding-agent/src/core/system-prompt.ts"
    "packages/coding-agent/src/core/model-resolver.ts"
    "packages/coding-agent/src/core/model-registry.ts"
)

: > "$OUTPUT"
for pattern in "${TRACKED_DIRS[@]}"; do
    target="$UPSTREAM/$pattern"
    if [ -f "$target" ]; then
        # Single file
        rel="${target#$UPSTREAM/}"
        hash=$(shasum -a 256 "$target" | cut -d' ' -f1)
        echo "$hash  $rel" >> "$OUTPUT"
    elif [ -d "$target" ]; then
        # Directory — hash all .ts files
        find "$target" -name "*.ts" -type f | sort | while read f; do
            rel="${f#$UPSTREAM/}"
            hash=$(shasum -a 256 "$f" | cut -d' ' -f1)
            echo "$hash  $rel" >> "$OUTPUT"
        done
    fi
done

# Sort for stable output
sort -k2 -o "$OUTPUT" "$OUTPUT"

echo "Recorded $(wc -l < "$OUTPUT" | tr -d ' ') file hashes to $OUTPUT"
