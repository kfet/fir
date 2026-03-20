# Package Manager Feature — Status

## What exists (already merged to main)

**Phase 1 — Go backend** (`pkg/pkg/`, `cmd/fir/install.go`) — ✅ merged, all tests green.

- `pkg/pkg/parse.go` — `ParseSource()` for git URLs (SSH, HTTPS, bare), local paths, `@ref` pins
- `pkg/pkg/git.go` — `Clone`, `CloneRef`, `Pull`, `CurrentRef`
- `pkg/pkg/resources.go` — `ScanPackageResources()` with `fir.json` manifest or auto-discovery
- `pkg/pkg/manager.go` — `Manager` with Install/Uninstall/Update/List/Resolve, canonical dedup
- `cmd/fir/install.go` — CLI subcommands: `fir install`, `fir uninstall`, `fir update`, `fir packages`
- `pkg/config/settings.go` — `GetGlobalPackages`, `GetProjectPackages`, `SetGlobalPackages`
- `pkg/resources/resourceloader.go` — packages contribute skills, prompts, extensions, themes
- `pkg/extension/discovery.go` — `DiscoverExtra()`, `ConfigsFromFiles()`
- `pkg/extension/manager.go` / `setup.go` — extra extension dirs/files from packages
- `cmd/fir/app.go` — wired package extensions + themes into session setup

## What is in progress

**Phase 2 — Builtin Python extension** `pkg/resources/builtin_extensions/install.py`

Status: **file written, `make all` is failing**.

### Failures

#### 1. `pkg/extension` tests — wrong tool counts (5 failing tests)

The new `install.py` builtin registers 4 tools (`install_package`, `uninstall_package`,
`list_packages`, `update_packages`). Tests in `pkg/extension/manager_test.go` were
written when only `aside` (1 tool) was a builtin, so their hardcoded expected counts
are all off by 4.

Failing tests and fixes needed:

| Test | Was | Should be |
|------|-----|-----------|
| `TestManager_StartStop` | `pollToolCount(2)` | `pollToolCount(6)` |
| `TestManager_UntrustedSkipped` | `n != 1` | `n != 5` |
| `TestManager_Reload` (before) | `pollToolCount(2)` / `n != 2` | `pollToolCount(6)` / `n != 6` |
| `TestManager_Reload` (after) | `pollToolCount(3)` / `n != 3` | `pollToolCount(7)` / `n != 7` |
| `TestManager_ReloadCallsUnregister` (before & after) | `pollToolCount(2)` / `n != 2` | `pollToolCount(6)` / `n != 6` |
| `TestManager_ActiveMode` | `pollToolCount(2)` / `n != 2` | `pollToolCount(6)` / `n != 6` |
| `TestManager_AllowedNames` | `n != 1` | unchanged (AllowedNames filters install out too) |

Fix: add a `const builtinToolCount = 5` (aside=1 + install=4) at top of
`manager_test.go` and use it everywhere the old magic numbers appear.

#### 2. `install.py` — unused `import sys` (ruff lint error)

Line 17 of `install.py` imports `sys` but never uses it. Ruff will fail lint.

Fix: remove `import sys`.

### Other known issues in install.py

- `# tools:` is not a valid frontmatter key (autoresearch.py doesn't declare tools in
  frontmatter either — only `# commands:` and `# events:` are parsed by the Go test).
  **Remove the `# tools:` line.**
- `@fir_ext.command()` without `name=` kwarg — autoresearch uses
  `@fir_ext.command(name="autoresearch", ...)`. Current rewrite already uses `name=`.
- `ctx.info()` / `ctx.error()` — need to verify these are real SDK methods (autoresearch
  uses `ctx.reply()` for progress messages). If `ctx.info` doesn't exist, switch to
  returning a dict response.

## Next steps (in order)

1. **Fix install.py**
   - Remove `import sys`
   - Remove `# tools:` frontmatter line (or replace with valid key)
   - Verify `ctx.info` / `ctx.error` exist in fir_ext SDK; if not, rewrite command
     handlers to return `{"message": "..."}` dicts

2. **Fix `pkg/extension/manager_test.go`**
   - Add `const builtinToolCount = 5` (1 aside + 4 install)
   - Replace all magic `2`, `3`, `1` tool count assertions with `builtinToolCount + N`

3. **Run `make all`** — must be fully green

4. **Commit**:
   `feat: add install builtin extension for in-session package management`

5. **Update `CHANGELOG.md`** under `## [Unreleased]` → `### Added`

## Key constraints

- Frontmatter completeness test (`TestBuiltinExtensionFrontmatterCompleteness`) scans
  all `.py` files, finds `@fir_ext.command(name="X")` decorators, and checks they
  exactly match the `# commands: X: desc` frontmatter entries. No extras, no missing.
- Ruff lint target: `.fir/extensions/install.py` (symlink to the builtin) — all Python
  style rules apply including RUF005 (no list concatenation with `+`).
- `make all` must pass fully before commit.
