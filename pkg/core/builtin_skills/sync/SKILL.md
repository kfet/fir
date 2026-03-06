---
name: sync
description: Sync a downstream port with upstream source changes. Detects changed files, applies equivalent changes, and updates the baseline.
---

# Upstream Sync

Sync a downstream port with the latest changes from the upstream source repository.

## Configuration

Before starting, identify:
- **Upstream repo path** — relative or absolute path to the upstream repository
- **Baseline file** — records the last-synced state (default: `sync/.baseline-hashes`)
- **File map** — maps upstream files to downstream equivalents (default: `sync/UPSTREAM_MAP.md`). When splitting a large downstream file into smaller ones, update this map so future syncs know where to apply upstream changes.
- **Sync log** — records sync history (default: `sync/SYNC_LOG.md`)
- **Check script** — detects changes (default: `sync/sync-check.sh`)
- **Record script** — updates baseline (default: `sync/sync-record.sh`)

---

## Step 1 — Detect changes

```bash
bash sync/sync-check.sh <UPSTREAM_PATH>
```

If output is `No upstream changes detected.` — you are done.

Otherwise you get lines like:

```
CHANGED: path/to/upstream/file.ts
```

---

## Step 2 — For each changed file, look up the downstream counterpart

Open the file map. Find the upstream path in the table. It tells you whether the file is a **normal port** or a **generator**.

### Normal port

1. Read the diff since the last baseline.
2. Read the current downstream file.
3. Apply equivalent changes, keeping the downstream language idiomatic.
4. Update the upstream hash comment at the top of the downstream file.
5. Run tests to verify.

### Generator file

If the downstream file is auto-generated from a generator script:

1. Check if the **generator script** itself changed. If so, apply logic changes to it first.
2. Re-run the generator.
3. Verify the build and tests pass.

### File not in the map

Check if it belongs to a component that isn't ported. If so, skip it. If it's a new file that *should* be ported, add it to the map and port it.

---

## Step 3 — Update the baseline

After all downstream files are updated and tests pass:

```bash
bash sync/sync-record.sh <UPSTREAM_PATH>
```

Verify it's clean:

```bash
bash sync/sync-check.sh <UPSTREAM_PATH>
# Expected: No upstream changes detected.
```

---

## Step 4 — Log the sync

Append an entry to the sync log:

```markdown
## YYYY-MM-DD — Sync to commit <short-hash>

- `path/to/changed/file` → `downstream/file`: One-line description of what changed.
```

---

## Rules

- **Never update the baseline before verifying the build and tests pass.**
- **Don't skip changed files.** If a file changed but the downstream is already correct, still update the baseline — but confirm it first.
- **One baseline update at the end**, not per-file.
- **Keep upstream hash comments accurate.** They are the source of truth for sync state.
