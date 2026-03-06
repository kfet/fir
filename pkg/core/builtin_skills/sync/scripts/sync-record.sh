#!/bin/bash
# Usage: ./.fir/skills/sync/sync-record.sh ../pi-mono
# Records hash baseline for all TS source files

UPSTREAM="$1"
OUTPUT=".fir/skills/sync/data/.baseline-hashes"

if [ -z "$UPSTREAM" ]; then
    echo "Usage: $0 <path-to-pi-mono>"
    exit 1
fi

find "$UPSTREAM/packages" -name "*.ts" -path "*/src/*" -type f | sort | while read f; do
    rel="${f#$UPSTREAM/}"
    hash=$(shasum -a 256 "$f" | cut -d' ' -f1)
    echo "$hash  $rel"
done > "$OUTPUT"

echo "Recorded $(wc -l < "$OUTPUT" | tr -d ' ') file hashes to $OUTPUT"
