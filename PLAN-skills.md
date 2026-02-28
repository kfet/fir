# Built-in Skills Implementation Plan

## Goal

Ship portable skills embedded in the fir binary so they're available out-of-the-box, with CLI and slash-command UIs for discovery and installation.

---

## 1. Embed built-in skills

**Portable skills** (no fir-project-specific references):
`overseer`, `research`, `review`, `fix`, `loop`, `monitor`, `notify`, `tmux-driver`, `claude-usage`, `skills`

**Excluded** (fir-project-specific): `e2e`, `release`, `sync`, `work`

### Files

- **`pkg/core/builtin_skills/`** — new directory, copy of each skill's directory tree:
  ```
  pkg/core/builtin_skills/
    overseer/SKILL.md
    research/SKILL.md
    review/SKILL.md
    fix/SKILL.md
    loop/SKILL.md
    monitor/SKILL.md
    notify/SKILL.md
    notify/scripts/notify.sh
    tmux-driver/SKILL.md
    tmux-driver/scripts/tmux-helpers.sh
    claude-usage/SKILL.md
    skills/SKILL.md
  ```
- **`pkg/core/builtin_skills.go`** — new file:
  ```go
  package core

  import "embed"

  //go:embed builtin_skills
  var BuiltinSkillsFS embed.FS
  ```

### Task breakdown
1. Copy skill dirs (SKILL.md + scripts/) into `pkg/core/builtin_skills/`.
2. Create `builtin_skills.go` with the embed directive.
3. Verify `go build` succeeds.

---

## 2. Third skill source `builtin`

Modify `LoadSkills()` in `pkg/core/skills.go` to load embedded skills as `source="builtin"` with lowest priority (user > project > builtin).

### Design

- Add `LoadBuiltinSkills() LoadSkillsResult` in `pkg/core/builtin_skills.go`:
  - Walk `BuiltinSkillsFS` to find `SKILL.md` files.
  - For each, parse frontmatter from the embedded content (reuse `parseFrontmatterSimple`).
  - Set `Source: "builtin"`, `FilePath` to the embed path.
  - **Companion scripts**: On first access (lazy), extract the entire skill dir to a temp directory so `$SKILL_DIR` / `BaseDir` paths work. Use `os.MkdirTemp("", "fir-builtin-skills-")`. Extract once per process; cache the temp dir path in a package-level var.

- In `LoadSkills()`, after user and project sources (and before `SkillPaths`), call:
  ```go
  if opts.IncludeDefaults {
      addSkills(LoadBuiltinSkills())
  }
  ```
  Since `addSkills` skips names already in `skillMap`, builtin skills automatically have lowest priority.

### New/modified files
- **`pkg/core/builtin_skills.go`** — add `LoadBuiltinSkills()`, temp-dir extraction logic.
- **`pkg/core/skills.go`** — add `addSkills(LoadBuiltinSkills())` call in `LoadSkills()`.
- **`pkg/core/builtin_skills_test.go`** — test that builtin skills load, user/project override them, temp extraction works.

### Task breakdown
1. Implement `LoadBuiltinSkills()` with embed.FS walking + frontmatter parsing.
2. Implement lazy temp-dir extraction for companion scripts.
3. Wire into `LoadSkills()`.
4. Add tests.

---

## 3. CLI subcommands

### `fir skills` / `fir skills list`

Plain-text table to stdout: name, source, description (truncated to terminal width).

### `fir skills install <name> [--user] [--force]`

Extract a builtin skill to:
- `.fir/skills/<name>/` (project, default)
- `~/.fir/agent/skills/<name>/` (`--user`)

Error if target dir exists (unless `--force`).

### Design

- **`cmd/fir/args.go`** — detect `skills` subcommand in `os.Args`:
  - Add `SkillsCommand *SkillsArgs` field to `Args`.
  - `SkillsArgs` struct: `Action string` ("list" or "install"), `Name string`, `User bool`, `Force bool`.
  - Parse after detecting `os.Args[1] == "skills"`.

- **`cmd/fir/app.go`** — dispatch before normal arg parsing (like `update`):
  ```go
  if len(os.Args) >= 2 && os.Args[1] == "skills" {
      return runSkills()
  }
  ```

- **`cmd/fir/skills.go`** — new file:
  - `runSkills() error` — parse subcommand args, dispatch to list/install.
  - `runSkillsList()` — load all skills via `LoadSkills()`, print table.
  - `runSkillsInstall(name string, user, force bool)` — find builtin by name, extract from `BuiltinSkillsFS` to target dir.

### New/modified files
- **`cmd/fir/skills.go`** — new, implements `runSkills`, `runSkillsList`, `runSkillsInstall`.
- **`cmd/fir/app.go`** — add early dispatch for `skills` subcommand.
- **`cmd/fir/args.go`** — add help text for `skills` subcommand.

### Task breakdown
1. Add `skills` dispatch in `app.go`.
2. Implement `skills.go` with list and install commands.
3. Update help text.
4. Add tests.

---

## 4. Slash command `/skills`

### Design

- `/skills` — list all loaded skills inline (name, source, description), similar to `/help`.
- `/skills install <name>` — extract builtin skill to project `.fir/skills/<name>/`, reload resources.

### New/modified files
- **`pkg/core/slashcmds.go`** — add `skills` to `BuiltinSlashCommands` list.
- **`pkg/modes/interactive/mode.go`** — add `/skills` case in `handleSlashCommand()`:
  - No args or `list`: format skill table, display as system message.
  - `install <name>`: extract builtin skill, reload resources, confirm.

### Task breakdown
1. Register `/skills` in `BuiltinSlashCommands`.
2. Implement handler in interactive mode.
3. Add autocomplete for `/skills install <name>`.
4. Add tests.

---

## Implementation order

1. **Embed** (§1) — copy files, create `builtin_skills.go`, verify build.
2. **Load** (§2) — `LoadBuiltinSkills()`, wire into `LoadSkills()`, tests.
3. **CLI** (§3) — `fir skills list`, `fir skills install`, tests.
4. **Slash** (§4) — `/skills` command, tests.

Each step is independently shippable and testable.

---

## File summary

| Action | File |
|--------|------|
| Create | `pkg/core/builtin_skills/` (skill dirs) |
| Create | `pkg/core/builtin_skills.go` |
| Create | `pkg/core/builtin_skills_test.go` |
| Modify | `pkg/core/skills.go` |
| Create | `cmd/fir/skills.go` |
| Modify | `cmd/fir/app.go` |
| Modify | `cmd/fir/args.go` |
| Modify | `pkg/core/slashcmds.go` |
| Modify | `pkg/modes/interactive/mode.go` |
| Modify | `CHANGELOG.md` |
