# Package Manager Feature — Status

## ✅ Complete

Both phases are done and merged to `main`.

---

## Phase 1 — Go backend (merged at `62e72f3`)

- `pkg/pkg/` — parse, git, resources, manager
- `cmd/fir/install.go` — `fir install/uninstall/update/packages` CLI subcommands
- `pkg/config/settings.go` — package list persistence
- `pkg/resources/resourceloader.go` — packages contribute skills, extensions, themes
- `pkg/extension/` — extra dirs/files wired in from packages
- `cmd/fir/app.go` — session setup wired with packages

## Phase 2 — Builtin extension (merged at `847c79e`)

- `pkg/resources/builtin_extensions/install.py` — builtin extension with:
  - Slash commands: `/install`, `/uninstall`, `/packages`, `/update`
  - AI tools: `install_package`, `uninstall_package`, `list_packages`, `update_packages`
- `pkg/extension/manager_test.go` — updated `builtinToolCount = 5` (aside=1 + install=4)
- `CHANGELOG.md` — entry added

## Key design decisions

- `# builtin: true` in frontmatter → loaded by `LoadBuiltinExtensions()`
- Commands return `{"message": "..."}` dict (not `ctx.info()` which doesn't exist)
- Extension shells out to `fir <subcommand>` binary using `FIR_BIN` env override
- All list construction uses `[x, *args]` not `[x] + args` (ruff RUF005)
