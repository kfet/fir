---
name: sync
description: Sync with an upstream source repo — detect changed files, apply equivalent downstream edits, update the baseline, and report any new features or non-trivial changes picked up.
---

# Upstream Sync

Sync with the latest changes from the upstream source repository.

## Configuration

Before starting, identify:
- **Upstream repo path** — relative or absolute path to the upstream repository
- **Baseline file** — records the last-synced state (default: `.fir/skills/sync/data/.baseline-hashes`)
- **File map** — maps upstream files to downstream equivalents (default: `.fir/skills/sync/data/UPSTREAM_MAP.md`). When splitting a large downstream file into smaller ones, update this map so future syncs know where to apply upstream changes.
- **Sync log** — records sync history (default: `.fir/skills/sync/data/SYNC_LOG.md`)
- **Check script** — detects changes (default: `.fir/skills/sync/scripts/sync-check.sh`)
- **Record script** — updates baseline (default: `.fir/skills/sync/scripts/sync-record.sh`)

---

## Step 1 — Pull latest upstream

Before detecting changes, pull the latest commits in the upstream repo:

```bash
git -C <UPSTREAM_PATH> pull --ff-only
```

If the pull fails (e.g. dirty working tree or diverged history), stop and inform the user.

---

## Step 2 — Detect changes

```bash
bash .fir/skills/sync/scripts/sync-check.sh <UPSTREAM_PATH>
```

If output is `No upstream changes detected.` — you are done.

Otherwise you get lines like:

```
CHANGED: path/to/upstream/file.ts
```

---

## Step 3 — For each changed file, look up the downstream counterpart

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

## Step 4 — Update the baseline

After all downstream files are updated and tests pass:

```bash
bash .fir/skills/sync/scripts/sync-record.sh <UPSTREAM_PATH>
```

Verify it's clean:

```bash
bash .fir/skills/sync/scripts/sync-check.sh <UPSTREAM_PATH>
# Expected: No upstream changes detected.
```

---

## Step 5 — Log the sync and summarise notable changes

Append an entry to the sync log:

```markdown
## YYYY-MM-DD — Sync to commit <short-hash>

- `path/to/changed/file` → `downstream/file`: One-line description of what changed.

### Notable changes
<!-- Only include this section when there are new features or non-trivial changes. -->
- **Feature / change title**: One or two sentences explaining what it does and why it matters.
```

After writing the log entry, **print a human-readable summary** of any new features or non-trivial behavioural changes that were brought in by this sync. Use the following format:

```
Sync summary — <short-hash> (<date>)

New features / non-trivial changes:
  • <Feature or change title>: <What it does and why it matters.>
  • ...

Trivial / housekeeping changes (skipped in summary):
  • <file>: <refactor / typo / comment / version bump / etc.>
```

Guidelines for classifying changes:
- **Notable** — new user-visible behaviour, new API surface, new CLI flag, changed defaults, performance improvements, security fixes, renamed/removed symbols that callers must update.
- **Trivial** — whitespace, comments, internal refactors with no external effect, dependency version bumps, test-only changes, log-message tweaks.

If no notable changes were picked up, still print the summary block with `(none)` so it is clear the sync was intentionally reviewed.

---

## Step 6 — Check project watch

After syncing, glance at `.fir/skills/sync/data/PROJECT_WATCH.md` for other projects worth checking. If it's been more than a week since the last look, quickly scan their recent releases for ideas relevant to fir.

---

## Rules

- **Never update the baseline before verifying the build and tests pass.**
- **Don't skip changed files.** If a file changed but the downstream is already correct, still update the baseline — but confirm it first.
- **One baseline update at the end**, not per-file.
- **Keep upstream hash comments accurate.** They are the source of truth for sync state.
