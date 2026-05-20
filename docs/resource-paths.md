# Resource Lookup Paths

fir discovers skills, themes, and extensions from multiple locations. This document explains the lookup order, how to configure additional paths via settings, and how path resolution works.

## Lookup Order

Resources are discovered from these sources:

| Priority | Source | Skills | Extensions | Themes |
|----------|--------|--------|------------|--------|
| 1 | CLI flags | `--skill <path>` | `--extension <name>` | `--theme <path>` |
| 2 | Project dir | `.fir/skills/` | `.fir/extensions/` | — |
| 3 | User dir | `<agent-dir>/skills/` (`~/.config/fir/skills/` by default) | `<agent-dir>/extensions/` (`~/.config/fir/extensions/` by default) | — |
| 4 | Settings paths | `"skills"` array | `"extensions"` array* | `"themes"` array |
| 5 | Packages | installed via `fir install` | installed via `fir install` | installed via `fir install` |
| 6 | Builtins | embedded in binary | embedded in binary | — |

\* The `"extensions"` setting is a **name allowlist**, not a path list. It filters which discovered extensions to activate. The other arrays (`"skills"`, `"themes"`) are **additional path lists**.

For skills, **same-named entries from different sources coexist by default**; each gets a unique ID of the form `<origin>__<name>` (e.g. `user__release`, `pkg_github_com_kfet_foo__release`). To replace another same-named skill, add `override: true` or `override: <full-id>` to its frontmatter. See the `self` skill for details.

## Configuring Additional Paths

Add paths to `"skills"` or `"themes"` in `settings.json` (global or project):

```jsonc
// ~/.config/fir/settings.json (global)
{
  "skills": ["skills", "~/shared-skills"],
  "themes": ["~/my-themes"]
}
```

```jsonc
// .fir/settings.json (project)
{
  "skills": ["/opt/team/skills", "extra-skills"]
}
```

Project settings merge on top of global settings. For array fields, the project value **replaces** the global value (standard fir merge behavior).

## Path Resolution

Three forms are supported:

| Form | Example | Resolves to |
|------|---------|-------------|
| Absolute | `/opt/shared/skills` | `/opt/shared/skills` |
| Home-relative | `~/my-skills` | `$HOME/my-skills` |
| Relative | `skills` | `$CWD/skills` |

### Why relative paths resolve against cwd

Relative paths resolve against the **current working directory** at startup — not against the settings file location. This is intentional and enables a powerful pattern:

```jsonc
// Global settings: ~/.config/fir/settings.json
{
  "skills": ["skills"]
}
```

With this single global setting, fir will look for a `skills/` directory in whatever project you're working in. Projects that have a `skills/` folder get those skills loaded automatically; projects that don't simply skip it (missing paths are silently ignored).

### XDG Support

If `$XDG_CONFIG_HOME` is set, the global config directory is `$XDG_CONFIG_HOME/fir/` instead of `~/.config/fir/`. The agent directory can also be overridden with `$FIR_AGENT_DIR` or per-invocation with `fir --agent-dir <dir>` (CLI flag wins over the environment variable).

## Examples

### Shared team skills across all projects

```jsonc
// ~/.config/fir/settings.json
{ "skills": ["skills"] }
```

Any project with a `skills/` directory will have those skills auto-loaded.

### Mix of global and project-specific skills

```jsonc
// ~/.config/fir/settings.json
{ "skills": ["~/company-skills"] }

// .fir/settings.json (in a specific project)
{ "skills": ["~/company-skills", "extra-skills"] }
```

The project config replaces the global `skills` array, so include all desired paths.
