# Upstream Sync Process

## Goal

When the TS repo changes, an agent (or human) should be able to:
1. See exactly which TS files changed
2. Find the corresponding Go files instantly
3. Apply equivalent changes
4. Record what was synced

## File Mapping Convention

Every Go file has a header comment:

```go
// Ported from: packages/ai/src/providers/anthropic.ts
// Upstream hash: 1caadb2e (commit at time of port)
```

The master map lives in `sync/UPSTREAM_MAP.md`.

## sync-check.sh

Script that compares upstream TS files against recorded baseline:

```bash
#!/bin/bash
# Usage: ./sync/sync-check.sh ../pi-mono
# Shows which TS files changed since last sync

UPSTREAM="$1"
BASELINE="sync/.baseline-hashes"

if [ ! -f "$BASELINE" ]; then
    echo "No baseline found. Run sync-record.sh first."
    exit 1
fi

while IFS='  ' read -r old_hash file; do
    if [ -f "$UPSTREAM/$file" ]; then
        new_hash=$(sha256sum "$UPSTREAM/$file" | cut -d' ' -f1)
        if [ "$old_hash" != "$new_hash" ]; then
            echo "CHANGED: $file"
        fi
    else
        echo "DELETED: $file"
    fi
done < "$BASELINE"
```

## sync-record.sh

Records current state as baseline:

```bash
#!/bin/bash
# Usage: ./sync/sync-record.sh ../pi-mono
# Records hash baseline for all TS source files

UPSTREAM="$1"
OUTPUT="sync/.baseline-hashes"

find "$UPSTREAM/packages" -name "*.ts" -path "*/src/*" -type f | sort | while read f; do
    rel="${f#$UPSTREAM/}"
    hash=$(sha256sum "$f" | cut -d' ' -f1)
    echo "$hash  $rel"
done > "$OUTPUT"

echo "Recorded $(wc -l < "$OUTPUT") file hashes to $OUTPUT"
```

## SYNC_LOG.md Format

```markdown
## 2025-02-08 — Initial port from commit 1caadb2e

- Phase 1 (AI layer) ported from packages/ai/src/

## 2025-03-15 — Sync to commit abc1234

- ai/providers/anthropic.ts → pkg/ai/providers/anthropic.go: Added cache retention TTL
- core/agent-session.ts → pkg/core/agentsession.go: Added retry backoff logic
- [SKIPPED] core/extensions/runner.ts: Extension system deferred
```

## Agent Resume Workflow

When an agent picks up work with clean context, it should:

1. **Read `docs/plan/04-phases.md`** to see where we are
2. **Read `docs/plan/07-work-tracker.md`** to see what's done/in-progress
3. **Read `sync/UPSTREAM_MAP.md`** to find the TS↔Go file mapping
4. **Read the specific TS source file** being ported
5. **Read existing Go files** in the same package for style/patterns
6. **Port the file**, adding the header comment
7. **Update `07-work-tracker.md`** marking the file done
