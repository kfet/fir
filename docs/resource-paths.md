# Resource Lookup Paths

fir discovers skills, prompts, themes, and extensions from multiple locations. This document explains the lookup order, how to configure additional paths via settings, and how path resolution works.

## Lookup Order

Resources are discovered in priority order. On name collisions, earlier sources win:

| Priority | Source | Skills | Prompts | Extensions | Themes |
|----------|--------|--------|---------|------------|--------|
| 1 | CLI flags | `--skill <path>` | `--prompt-template <path>` | `--extension <name>` | `--theme <path>` |
| 2 | Project dir | `.fir/skills/` | `.fir/prompts/` | `.fir/extensions/` | — |
| 3 | User dir | `~/.config/fir/skills/` | `~/.config/fir/prompts/` | `~/.config/fir/extensions/` | — |
| 4 | Settings paths | `"skills"` array | `"prompts"` array | `"extensions"` array* | `"themes"` array |
| 5 | Packages | installed via `fir install` | installed via `fir install` | installed via `fir install` | installed via `fir install` |
| 6 | Builtins | embedded in binary | — | embedded in binary | — |

\* The `"extensions"` setting is a **name allowlist**, not a path list. It filters which discovered extensions to activate. The other arrays (`"skills"`, `"prompts"`, `"themes"`) are **additional path lists**.

## Configuring Additional Paths

Add paths to `"skills"`, `"prompts"`, or `"themes"` in `settings.json` (global or project):

```jsonc
// ~/.config/fir/settings.json (global)
{
  "skills": ["skills", "~/shared-skills"],
  "prompts": ["prompts"],
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

This lets you establish team conventions like:
- `skills/` — project-specific agent skills
- `prompts/` — project-specific prompt templates

...and have fir discover them everywhere without per-project `.fir/settings.json` configuration.

### XDG Support

If `$XDG_CONFIG_HOME` is set, the global config directory is `$XDG_CONFIG_HOME/fir/` instead of `~/.config/fir/`. The agent directory can also be overridden with `$FIR_AGENT_DIR`.

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

### Custom prompt templates

```jsonc
// ~/.config/fir/settings.json
{ "prompts": ["prompts", "~/shared-prompts"] }
```

Looks for `prompts/` in the current project and `~/shared-prompts/` globally.
