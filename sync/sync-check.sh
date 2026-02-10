#!/bin/bash
# Usage: ./sync/sync-check.sh ../pi-mono
# Shows which TS files changed since last sync

UPSTREAM="$1"
BASELINE="sync/.baseline-hashes"

if [ -z "$UPSTREAM" ]; then
    echo "Usage: $0 <path-to-pi-mono>"
    exit 1
fi

if [ ! -f "$BASELINE" ]; then
    echo "No baseline found. Run sync-record.sh first."
    exit 1
fi

changed=0
while IFS='  ' read -r old_hash file; do
    if [ -f "$UPSTREAM/$file" ]; then
        new_hash=$(shasum -a 256 "$UPSTREAM/$file" | cut -d' ' -f1)
        if [ "$old_hash" != "$new_hash" ]; then
            echo "CHANGED: $file"
            changed=$((changed + 1))
        fi
    else
        echo "DELETED: $file"
        changed=$((changed + 1))
    fi
done < "$BASELINE"

if [ "$changed" -eq 0 ]; then
    echo "No upstream changes detected."
fi
