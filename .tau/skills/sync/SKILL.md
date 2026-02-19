---
name: sync
description: Sync the Go port with upstream TypeScript changes from pi-mono. Detects changed files, applies equivalent Go changes, regenerates models if needed, and updates the baseline.
---

# Upstream Sync

Sync the Go port with the latest upstream TypeScript changes from `../pi-mono`.

## Quick reference

- Upstream repo: `../pi-mono` (relative to tau root)
- Baseline: `sync/.baseline-hashes`
- File map: `sync/UPSTREAM_MAP.md`
- Log: `sync/SYNC_LOG.md`

---

## Step 1 — Detect changes

```bash
bash sync/sync-check.sh ../pi-mono
```

If output is `No upstream changes detected.` — you are done.

Otherwise you get lines like:

```
CHANGED: packages/ai/src/models.generated.ts
CHANGED: packages/coding-agent/src/core/auth-storage.ts
```

---

## Step 2 — For each changed file, look up the Go counterpart

Open `sync/UPSTREAM_MAP.md`. Find the TS path in the table. It tells you whether the file is a **normal port** or a **generator**.

### Normal port

1. Read the diff since the last baseline commit:
   ```bash
   # Find the hash recorded for this file in the baseline
   grep "packages/coding-agent/src/core/auth-storage.ts" sync/.baseline-hashes
   # Diff from that hash to HEAD
   cd ../pi-mono && git diff <old-hash>..HEAD -- packages/coding-agent/src/core/auth-storage.ts
   ```
   Or just diff from the HEAD of the Go file's recorded upstream hash:
   ```bash
   grep "Upstream hash" pkg/core/authstorage.go   # get the recorded hash
   cd ../pi-mono && git diff <recorded-hash>..HEAD -- packages/coding-agent/src/core/auth-storage.ts
   ```

2. Read the current Go file.

3. Apply equivalent changes. Keep Go idiomatic — no 1:1 TS syntax. Go uses:
   - `NewFoo(...)` factory functions instead of `static Foo.create(...)`
   - `sync.Mutex` instead of file locks (unless real cross-process locking is needed)
   - Synchronous writes with goroutines if async is needed (rarely)

4. Update the `// Upstream hash:` comment at the top of the Go file to the current HEAD:
   ```bash
   cd ../pi-mono && git rev-parse --short HEAD
   ```

5. Run tests:
   ```bash
   go vet ./... && go test ./...
   ```

### Generator file (`ai/src/models.generated.ts` → `pkg/ai/models_generated.go`)

The generated file's content comes from fetching live APIs plus hardcoded overrides in `cmd/generate-models/main.go`. Follow this order:

1. Check if the **generator script** itself changed:
   ```bash
   grep "Upstream hash" cmd/generate-models/main.go   # get recorded hash
   cd ../pi-mono && git log --oneline <recorded-hash>..HEAD -- packages/ai/scripts/generate-models.ts
   ```

2. **If the generator script changed:** Apply equivalent logic changes to `cmd/generate-models/main.go` first, then regenerate. Update `// Upstream hash:` in the generator.

3. **If only the output changed** (new models added by external API or upstream data): Just re-run the generator — no Go code changes needed.

4. Regenerate:
   ```bash
   make generate-models
   ```

5. Verify:
   ```bash
   go build ./... && go test ./pkg/ai/...
   ```

### File not in UPSTREAM_MAP.md

Check if it belongs to a package we don't port (`packages/mom`, `packages/tui-server`, etc.). If so, skip it — no Go action needed.

If it's a new file that *should* be ported, add it to `UPSTREAM_MAP.md` and port it following the normal port process.

---

## Step 3 — Update the baseline

After all Go files are updated and tests pass:

```bash
bash sync/sync-record.sh ../pi-mono
```

Verify it's clean:

```bash
bash sync/sync-check.sh ../pi-mono
# Expected: No upstream changes detected.
```

---

## Step 4 — Log the sync

Append an entry to `sync/SYNC_LOG.md`:

```markdown
## YYYY-MM-DD — Sync to commit <short-hash>

- `path/to/changed.ts` → `pkg/foo/bar.go`: One-line description of what changed.
- `models.generated.ts` → `pkg/ai/models_generated.go`: Added N new models (list notable ones).
```

---

## Common patterns

### TS `static Foo.create(path?)` → Go `NewFoo(path string)`

Upstream often refactors constructors to factory methods. In Go, keep the `NewFoo` convention.

### TS `AuthStorage.inMemory(data)` → Go `NewInMemoryAuthStorage(data)`

In-memory variants are used in tests. Same pattern — just a `NewFooInMemory` or `NewInMemory` helper.

### TS `async withLockAsync(fn)` → Go `WithLockAsync(fn func(...) (..., error))`

Go doesn't have async/await. Use a synchronous callback with an `error` return; the caller runs it inline under a `sync.Mutex`.

### TS `drainErrors()` → Go `DrainErrors()`

Go uses exported PascalCase. The pattern (drain + clear a slice of accumulated errors) is identical.

### Barrel file changes (`src/index.ts`)

These only export new TS types. In Go, all types are already directly accessible from their packages. No action needed.

---

## Rules

- **Never update the baseline before verifying `go build ./... && go test ./...` passes.**
- **Don't skip changed files.** If a file changed but the Go side is already correct (e.g., we anticipated the change), still update the baseline — but confirm it's really in sync first.
- **One baseline update at the end**, not per-file. Run `sync-record.sh` once after all files are done.
- **Keep `// Upstream hash:` accurate.** It's the single source of truth for "what state of the TS source does this Go file reflect."
